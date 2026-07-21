package helper

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

type appRollbackCmd struct {
	App           string            `arg:"" help:"App name."`
	Env           string            `arg:"" help:"Env name."`
	Release       string            `arg:"" optional:"" help:"Release to run. Omitted = previous local release."`
	SSHKeyComment string            `name:"ssh-key-comment" help:"SSH public key comment for the deploying key."`
	GitAuthor     string            `name:"git-author" help:"Git author configured by the deploying client."`
	ActivationID  string            `kong:"-"`
	Target        artifactCandidate `kong:"-"`
}

var appendRollbackDeployJournal = appendDeployJournalEntry

func (c appRollbackCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	if c.Release != "" {
		if err := validateRelease(c.Release); err != nil {
			utils.DieError(err, 1)
		}
	}
	authorizeOrDie(helperVerbRollback, authTargetForAppEnv(c.App, c.Env, "rollback"))
	withAppEnvLock(c.App, c.Env, func() { c.runLocked() })
	return nil
}

func (c appRollbackCmd) runLocked() {
	startedAt := time.Now().UTC()
	result, err := c.rollbackRelease(startedAt)
	if err != nil {
		if result.Committed {
			var degraded committedDegradedError
			if errors.As(err, &degraded) {
				c.recordRollbackDegraded(result, startedAt, degraded.Err)
			} else {
				c.recordRollbackFailure(result, startedAt, err)
			}
			utils.DieError(rollbackCommittedError(err), 1)
		}
		removePreparedCandidates(c.App, c.Env, c.ActivationID)
		utils.DieError(fmt.Errorf("nothing changed: %w", err), 1)
	}
}

func rollbackCommittedError(err error) error {
	var degraded committedDegradedError
	if errors.As(err, &degraded) {
		return newDeployCommittedDegradedError(degraded.Err)
	}
	return newDeployCommittedUnconvergedError(err)
}

func (c appRollbackCmd) recordRollbackDegraded(result rollbackPayload, startedAt time.Time, err error) {
	entry, _ := committedOutcomeJournalEntry(c.App, c.Env, activationrecords.CommittedDegraded, result.Previous, result.Release, c.actor(), startedAt, "durability", &result.Artifact, err)
	entry.Member = currentServerMemberForJournal()
	if appendErr := appendRollbackDeployJournal(c.App, c.Env, entry, nil); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; next: ship box doctor\n", appendErr)
	}
}

func (c appRollbackCmd) recordRollbackFailure(result rollbackPayload, startedAt time.Time, err error) {
	entry, _ := committedOutcomeJournalEntry(c.App, c.Env, activationrecords.CommittedUnconverged, result.Previous, result.Release, c.actor(), startedAt, committedFailureStep(err, "converge"), &result.Artifact, err)
	entry.Member = currentServerMemberForJournal()
	if appendErr := appendRollbackDeployJournal(c.App, c.Env, entry, nil); appendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write deploy journal: %v; next: ship box doctor\n", appendErr)
	}
}

func (c appRollbackCmd) recordRollbackSuccess(result rollbackPayload, startedAt time.Time) {
	entry := activationrecords.JournalEntry{Outcome: activationrecords.RolledBack, StartedAt: startedAt.Format(time.RFC3339Nano), EndedAt: time.Now().UTC().Format(time.RFC3339Nano), PreviousRelease: result.Previous, AttemptedRelease: result.Release, Activation: c.ActivationID, Identity: c.actor(), Member: currentServerMemberForJournal(), Artifact: &result.Artifact}
	if err := appendRollbackDeployJournal(c.App, c.Env, entry, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: rollback succeeded but failed to write deploy journal %s: %v; cleanup/GC were skipped; next: ship box doctor\n", identity.DeployJournalFile(c.App, c.Env), err)
	} else {
		removeContainers(result.StaleContainers)
		bestEffortGCAfterLifecycle(c.App, c.Env)
	}
	fmt.Print(renderRollbackText(result))
}

func (c appRollbackCmd) actor() activationrecords.Identity {
	return deployActor(c.SSHKeyComment, c.GitAuthor)
}

func (c *appRollbackCmd) rollbackRelease(startedAt time.Time) (rollbackPayload, error) {
	pointer, err := readActive(c.App, c.Env)
	if err != nil {
		return rollbackPayload{}, err
	}
	target, err := retainedArtifactForRollback(c.App, c.Env, c.Release, pointer)
	if err != nil {
		return rollbackPayload{}, err
	}
	if target.Tuple == pointer.Artifact {
		return rollbackPayload{}, fmt.Errorf("%s is already active", target.Tuple.DisplayIdentity())
	}
	if target.Tuple.StaticHash != "" {
		hash, hashErr := activationrecords.StaticTreeHash(staticReleasePath(c.App, c.Env, target.Tuple.Release, target.Tuple.StaticHash))
		if hashErr != nil || hash != target.Tuple.StaticHash {
			return rollbackPayload{}, fmt.Errorf("rollback target %s changed on disk", target.Tuple.DisplayIdentity())
		}
	}
	app := target.Resolved.Context
	app.StaticHash = target.Tuple.StaticHash
	if err := attachPreviewProtection(c.App, c.Env, app); err != nil {
		return rollbackPayload{}, err
	}
	if err := addConfiguredPreviewAlias(c.App, c.Env, app); err != nil {
		return rollbackPayload{}, err
	}
	if target.Tuple.ImageID != "" {
		c.ActivationID, err = newActivationID(c.App, c.Env, target.Tuple.Release)
		if err != nil {
			return rollbackPayload{}, err
		}
		resolved, resolveErr := resolveEnv(c.App, c.Env, app.Vars, app.SecretRefs)
		if resolveErr != nil {
			return rollbackPayload{}, resolveErr
		}
		for key, value := range shipInjectedEnv(c.App, c.Env, target.Tuple.Release, app) {
			resolved[key] = value
		}
		if _, err := writeActivationEnvFile(c.App, c.Env, c.ActivationID, resolved); err != nil {
			return rollbackPayload{}, err
		}
		containers, err := podmanPSContainers(c.App, c.Env)
		if err != nil {
			return rollbackPayload{}, err
		}
		started, err := startReleaseProcesses(startReleaseProcessesParams{App: c.App, Env: c.Env, Release: target.Tuple.Release, Activation: c.ActivationID, ImageID: target.Tuple.ImageID, EnvFile: identity.ActivationEnvFile(c.App, c.Env, c.ActivationID), ScrubValues: collectEnvValues(resolved), Context: app, OnlyPortful: true, ContainerName: func(proc string, _ config.Process) string {
			return nextProcessContainerName(containers, c.App, c.Env, proc, target.Tuple.Release, "rollback")
		}})
		if err != nil {
			return rollbackPayload{}, err
		}
		if err := validateAppCaddy(caddyfilePath(c.App, c.Env), c.App, c.Env, app, target.Tuple.Release, started.ProcessName); err != nil {
			return rollbackPayload{}, err
		}
	} else if err := validateAppCaddy(caddyfilePath(c.App, c.Env), c.App, c.Env, app, target.Tuple.Release, nil); err != nil {
		return rollbackPayload{}, err
	}
	payload := rollbackPayload{App: c.App, Env: c.Env, Previous: pointer.Artifact.Release, Release: target.Tuple.Release, Identity: target.Tuple.DisplayIdentity(), Artifact: target.Tuple, Processes: processNames(app.Processes)}
	fmt.Printf("Rollback target: %s\n", payload.Identity)
	payload.Committed, err = commitAndConverge(c.App, c.Env, activationrecords.Pointer{Version: 2, Activation: c.ActivationID, Artifact: target.Tuple}, func(stale []string) { payload.StaleContainers = uniqueContainerNames(stale) }, func() error { c.recordRollbackSuccess(payload, startedAt); return nil })
	return payload, err
}

type rollbackPayload struct {
	App             string                  `json:"app"`
	Env             string                  `json:"env"`
	Previous        string                  `json:"previous"`
	Release         string                  `json:"release"`
	Identity        string                  `json:"identity"`
	Artifact        activationrecords.Tuple `json:"artifact"`
	Processes       []string                `json:"processes"`
	Committed       bool                    `json:"-"`
	StaleContainers []string                `json:"-"`
}

func processNames(processes map[string]config.Process) []string { return sortedKeys(processes) }

func renderRollbackText(payload rollbackPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rolled back %s (%s) from %s to %s\n", payload.App, payload.Env, payload.Previous, payload.Release)
	for _, proc := range payload.Processes {
		fmt.Fprintf(&b, "  %-12s running\n", proc)
	}
	return b.String()
}
