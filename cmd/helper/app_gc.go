package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/podmanruntime"
	"github.com/fprl/ship/internal/utils"
)

const gcGracePeriod = 10 * time.Minute

var gcNow = time.Now

func bestEffortGCAfterLifecycle(app, env string) string {
	summary, err := gcEnv(app, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GC failed for %s (%s): %s\n", app, env, singleLine(err.Error()))
	}
	return strings.Join(summary.Removed, ", ")
}

type gcCmd struct {
	MemberFingerprint string `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	App               string `arg:"" optional:"" help:"Optional app name."`
	Env               string `arg:"" optional:"" help:"Optional env name."`
	JSON              bool   `name:"json" help:"Emit a structured GC summary."`
}

type gcSummary struct {
	App           string   `json:"app"`
	Env           string   `json:"env"`
	ActiveRelease string   `json:"active_release,omitempty"`
	KeptReleases  []string `json:"kept_releases,omitempty"`
	Removed       []string `json:"removed,omitempty"`
	Absent        []string `json:"absent,omitempty"`
	Skipped       []string `json:"skipped,omitempty"`
	Failures      []string `json:"failures,omitempty"`
}

type gcBoxSummary struct {
	Environments []gcSummary `json:"environments"`
	Removed      []string    `json:"removed,omitempty"`
	Absent       []string    `json:"absent,omitempty"`
	Skipped      []string    `json:"skipped,omitempty"`
	Failures     []string    `json:"failures,omitempty"`
}

func (c gcCmd) BeforeApply() error { return requireRoot() }

func (c gcCmd) Run() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	authorizeOrDie(helperVerbBoxMutation, authTargetForBox("gc box"))
	if (c.App == "") != (c.Env == "") {
		utils.DieError(errors.New("server gc requires both <app> and <env>, or neither"), 2)
	}
	var result any
	var runErr error
	if c.App != "" {
		if err := validateAppEnv(c.App, c.Env); err != nil {
			utils.DieError(err, 2)
		}
		var summary gcSummary
		withAppEnvLock(c.App, c.Env, func() { summary, runErr = gcEnv(c.App, c.Env) })
		result = summary
	} else {
		result, runErr = gcAllEnvs()
	}
	if c.JSON {
		buf, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
	} else {
		fmt.Print(renderGCSummary(result))
	}
	if runErr != nil {
		utils.DieError(runErr, 1)
	}
	return nil
}

func renderGCSummary(value any) string {
	var b strings.Builder
	switch summary := value.(type) {
	case gcSummary:
		fmt.Fprintf(&b, "GC %s (%s)\n", summary.App, summary.Env)
		if len(summary.KeptReleases) > 0 {
			fmt.Fprintf(&b, "kept: %s\n", strings.Join(summary.KeptReleases, ", "))
		}
		for _, item := range summary.Removed {
			fmt.Fprintf(&b, "removed: %s\n", item)
		}
		for _, item := range summary.Absent {
			fmt.Fprintf(&b, "absent: %s\n", item)
		}
		for _, item := range summary.Skipped {
			fmt.Fprintf(&b, "skipped: %s\n", item)
		}
		for _, item := range summary.Failures {
			fmt.Fprintf(&b, "failed: %s\n", item)
		}
	case gcBoxSummary:
		for _, env := range summary.Environments {
			b.WriteString(renderGCSummary(env))
		}
		for _, item := range summary.Removed {
			fmt.Fprintf(&b, "removed: %s\n", item)
		}
		for _, item := range summary.Absent {
			fmt.Fprintf(&b, "absent: %s\n", item)
		}
		for _, item := range summary.Skipped {
			fmt.Fprintf(&b, "skipped: %s\n", item)
		}
		for _, item := range summary.Failures {
			fmt.Fprintf(&b, "failed: %s\n", item)
		}
	}
	return b.String()
}

func gcAllEnvs() (gcBoxSummary, error) {
	envs, err := identityAppEnvs()
	if err != nil {
		return gcBoxSummary{}, fmt.Errorf("enumerate app envs for GC: %w", err)
	}
	result := gcBoxSummary{Environments: make([]gcSummary, 0, len(envs))}
	for _, item := range envs {
		lock, lockErr := acquireAppEnvLock(item.App, item.Env)
		if lockErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s/%s: %v", item.App, item.Env, lockErr))
			continue
		}
		summary, envErr := gcEnv(item.App, item.Env)
		_ = lock.Release()
		result.Environments = append(result.Environments, summary)
		result.Absent = append(result.Absent, summary.Absent...)
		if envErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s/%s: %v", item.App, item.Env, envErr))
		}
	}
	if len(result.Failures) > 0 {
		return result, fmt.Errorf("GC completed with %d failure(s)", len(result.Failures))
	}
	return result, nil
}

func gcEnv(app, env string) (gcSummary, error) {
	if err := validateAppEnv(app, env); err != nil {
		return gcSummary{}, err
	}
	pointer, err := readActive(app, env)
	if errcat.Is(err, errcat.CodeNoDeploys) {
		return gcSummary{App: app, Env: env, Skipped: []string{"no committed release; nothing to collect"}}, nil
	}
	if err != nil {
		return gcSummary{App: app, Env: env}, fmt.Errorf("read active.json: %w", err)
	}
	summary := gcSummary{App: app, Env: env, ActiveRelease: pointer.Artifact.Release}
	if pointer.IsLegacy() {
		summary.Skipped = append(summary.Skipped, "legacy activation; redeploy to heal")
		return summary, nil
	}
	set, err := sharedArtifactCandidatesWithPointer(app, env, pointer)
	if err != nil {
		return summary, err
	}
	if set.Torn {
		warnTornDeployJournal(identity.DeployJournalFile(app, env))
		summary.Failures = append(summary.Failures, "deploy journal has an incomplete final entry")
		return summary, fmt.Errorf("deploy journal is incomplete")
	}
	kept := map[activationrecords.Tuple]bool{pointer.Artifact: true}
	for _, candidate := range set.Verified {
		kept[candidate.Tuple] = true
		summary.KeptReleases = append(summary.KeptReleases, candidate.Tuple.DisplayIdentity())
	}
	for _, tuple := range set.Protected {
		kept[tuple] = true
		summary.KeptReleases = append(summary.KeptReleases, tuple.DisplayIdentity())
	}
	for _, tuple := range set.Absent {
		summary.Absent = append(summary.Absent, tuple.DisplayIdentity())
	}
	sort.Strings(summary.KeptReleases)
	sort.Strings(summary.Absent)
	protectedImages := map[string]bool{}
	for tuple := range kept {
		if tuple.ImageID != "" {
			protectedImages[normalizeImageID(tuple.ImageID)] = true
		}
	}
	gcRemoveContainers(app, env, &summary)
	gcRemoveImages(app, env, protectedImages, &summary)
	gcRemoveStatic(app, env, kept, set.All, &summary)
	gcRemoveActivations(app, env, pointer, &summary)
	gcRemoveTempDirs(app, env, &summary)
	if len(summary.Removed) > 0 {
		err := appendDeployJournalEntry(app, env, deployJournalEntry{Outcome: activationrecords.GC, StartedAt: gcNow().UTC().Format(time.RFC3339Nano), EndedAt: gcNow().UTC().Format(time.RFC3339Nano), AttemptedRelease: pointer.Artifact.Release, GC: strings.Join(summary.Removed, ", "), Identity: deployActor("", "")}, nil)
		if err != nil {
			return summary, err
		}
	}
	if len(summary.Failures) > 0 {
		return summary, fmt.Errorf("GC completed with %d cleanup failure(s)", len(summary.Failures))
	}
	return summary, nil
}

func gcRemoveContainers(app, env string, summary *gcSummary) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		summary.Failures = append(summary.Failures, "containers: "+err.Error())
		return
	}
	for _, entry := range entries {
		if len(entry.Names) == 0 || entry.Labels["ship.app"] != app || entry.Labels["ship.env"] != env {
			continue
		}
		if entry.State == "running" {
			continue
		}
		name := entry.Names[0]
		if err := podmanruntime.CLI().RemoveContainer(name); err != nil {
			summary.Failures = append(summary.Failures, name+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "container "+name)
	}
}

func gcRemoveImages(app, env string, protected map[string]bool, summary *gcSummary) {
	images, err := podmanAllImagesForEnv(app, env)
	if err != nil {
		summary.Failures = append(summary.Failures, "images: "+err.Error())
		return
	}
	if containers, containerErr := podmanPSContainers(app, env); containerErr == nil {
		if used, inspectErr := podmanContainerImageIDs(containers); inspectErr == nil {
			for _, imageID := range used {
				protected[normalizeImageID(imageID)] = true
			}
		} else {
			summary.Failures = append(summary.Failures, "container image identity: "+inspectErr.Error())
			return
		}
	} else {
		summary.Failures = append(summary.Failures, "containers: "+containerErr.Error())
		return
	}
	// podman images reports one row per repo:tag with a shared Id, so the
	// sweep groups physically: skip decisions and the final remove-by-ID
	// happen once per image, after every ship-owned tag is untagged.
	removedIDs := map[string]bool{}
	skippedIDs := map[string]bool{}
	for _, image := range images {
		id := normalizeImageID(image.ImageID)
		if removedIDs[id] || skippedIDs[id] {
			continue
		}
		if protected[id] || freshAt(image.CreatedAt) {
			skippedIDs[id] = true
			summary.Skipped = append(summary.Skipped, "image "+image.ImageID)
			continue
		}
		shipTags := map[string]bool{}
		for _, row := range images {
			if normalizeImageID(row.ImageID) == id {
				for _, tag := range row.ShipTags {
					shipTags[tag] = true
				}
			}
		}
		image.ShipTags = sortedKeys(shipTags)
		removedIDs[id] = true
		tagFailed := false
		for _, tag := range image.ShipTags {
			if err := podmanruntime.CLI().RemoveImage(tag); err != nil {
				summary.Failures = append(summary.Failures, "image tag "+tag+": "+err.Error())
				tagFailed = true
				break
			}
		}
		if tagFailed {
			continue
		}
		if err := podmanruntime.CLI().RemoveImage(image.ImageID); err != nil {
			summary.Failures = append(summary.Failures, "image "+image.ImageID+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "image "+image.ImageID)
	}
}

func freshAt(createdAt time.Time) bool {
	return !createdAt.IsZero() && gcNow().Sub(createdAt) < gcGracePeriod
}

func gcRemoveStatic(app, env string, kept map[activationrecords.Tuple]bool, all []artifactCandidate, summary *gcSummary) {
	root := filepath.Join(identity.StaticDir(app, env), "releases")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		summary.Failures = append(summary.Failures, "static releases: "+err.Error())
		return
	}
	protectedPaths := map[string]bool{}
	protectedEnvelopes := map[string]bool{}
	for tuple := range kept {
		if tuple.StaticHash != "" {
			protectedPaths[staticReleasePath(app, env, tuple.Release, tuple.StaticHash)] = true
		}
		if tuple.EnvelopeHash != "" {
			protectedEnvelopes[tuple.EnvelopeHash] = true
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ship-release-") {
			hash := strings.TrimPrefix(entry.Name(), ".ship-release-")
			path := filepath.Join(root, entry.Name())
			if protectedEnvelopes[hash] || freshPath(path) {
				continue
			}
			if err := os.Remove(path); err != nil {
				summary.Failures = append(summary.Failures, "static envelope "+path+": "+err.Error())
				continue
			}
			summary.Removed = append(summary.Removed, "static envelope "+path)
			continue
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if protectedPaths[path] {
			continue
		}
		if freshPath(path) {
			summary.Skipped = append(summary.Skipped, "static "+path)
			continue
		}
		committed := false
		for _, candidate := range all {
			if candidate.Tuple.StaticHash != "" && staticReleasePath(app, env, candidate.Tuple.Release, candidate.Tuple.StaticHash) == path {
				committed = true
				break
			}
		}
		if committed {
			hash, hashErr := activationrecords.StaticTreeHash(path)
			if hashErr != nil || !strings.HasSuffix(entry.Name(), "-"+hash) {
				summary.Skipped = append(summary.Skipped, "protected static "+path)
				continue
			}
		}
		if err := os.RemoveAll(path); err != nil {
			summary.Failures = append(summary.Failures, "static "+path+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "static "+path)
	}
}

func gcRemoveActivations(app, env string, pointer activationrecords.Pointer, summary *gcSummary) {
	root := identity.ActivationsDir(app, env)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		summary.Failures = append(summary.Failures, "activations: "+err.Error())
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".env") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".env")
		path := filepath.Join(root, entry.Name())
		if id == pointer.Activation || freshPath(path) {
			summary.Skipped = append(summary.Skipped, "activation "+path)
			continue
		}
		if err := os.Remove(path); err != nil {
			summary.Failures = append(summary.Failures, "activation "+path+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "activation "+path)
	}
}

func gcRemoveTempDirs(app, env string, summary *gcSummary) {
	for _, root := range []string{identity.StaticDir(app, env), identity.EnvRoot(app, env)} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !gcOwnsTempDir(root, identity.EnvRoot(app, env), entry.Name()) {
				continue
			}
			if freshPath(filepath.Join(root, entry.Name())) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				summary.Failures = append(summary.Failures, "temp "+path+": "+err.Error())
				continue
			}
			summary.Removed = append(summary.Removed, "temp "+path)
		}
	}
}

func gcOwnsTempDir(root, envRoot, name string) bool {
	prefixes := []string{".staging-"}
	if root == envRoot {
		prefixes = append(prefixes, ".data-fork-", ".data-save-", ".data-restore-")
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func freshPath(path string) bool {
	info, err := os.Stat(path)
	return err == nil && freshAt(info.ModTime())
}
