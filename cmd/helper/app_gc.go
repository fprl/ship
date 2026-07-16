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

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

const gcGracePeriod = 10 * time.Minute
const globalDeployTempGracePeriod = 24 * time.Hour

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
	Skipped       []string `json:"skipped,omitempty"`
	Failures      []string `json:"failures,omitempty"`
}

type gcBoxSummary struct {
	Environments []gcSummary `json:"environments"`
	Removed      []string    `json:"removed,omitempty"`
	Skipped      []string    `json:"skipped,omitempty"`
	Failures     []string    `json:"failures,omitempty"`
}

func (c gcCmd) BeforeApply() error { return requireRoot() }

func (c gcCmd) Run() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	authorizeOrDie(helperVerbBoxMutation, authTargetForBox("gc box", gcTargetArgs(c.App, c.Env)...))
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

func gcTargetArgs(app, env string) []string {
	if app == "" && env == "" {
		return nil
	}
	return []string{"app=" + app, "env=" + env}
}

func renderGCSummary(value any) string {
	var b strings.Builder
	switch summary := value.(type) {
	case gcSummary:
		if len(summary.KeptReleases) == 0 {
			fmt.Fprintf(&b, "GC %s (%s)\n", summary.App, summary.Env)
		} else {
			fmt.Fprintf(&b, "GC %s (%s): kept %s\n", summary.App, summary.Env, strings.Join(summary.KeptReleases, ", "))
		}
		for _, item := range summary.Removed {
			fmt.Fprintf(&b, "removed: %s\n", item)
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
		var summary gcSummary
		lock, lockErr := acquireAppEnvLock(item.App, item.Env)
		if lockErr != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s/%s: %v", item.App, item.Env, lockErr))
			continue
		}
		summary, err = gcEnv(item.App, item.Env)
		_ = lock.Release()
		result.Environments = append(result.Environments, summary)
		if err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s/%s: %v", item.App, item.Env, err))
		}
	}
	gcRemoveGlobalDeployTemps(&result)
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
		summary := gcSummary{App: app, Env: env}
		summary.Skipped = append(summary.Skipped, "no committed release; nothing to collect")
		return summary, nil
	}
	if err != nil {
		return gcSummary{App: app, Env: env}, fmt.Errorf("read active.json: %w", err)
	}
	entries, torn, journalErr := readDeployJournalEntriesWithStatus(app, env)
	summary := gcSummary{App: app, Env: env, ActiveRelease: pointer.Release, KeptReleases: []string{pointer.Release}}
	if torn {
		warnTornDeployJournal(identity.DeployJournalFile(app, env))
		err := fmt.Errorf("deploy journal is incomplete; GC skipped for %s (%s)", app, env)
		summary.Failures = append(summary.Failures, err.Error())
		return summary, err
	}
	if journalErr != nil && !errcat.Is(journalErr, errcat.CodeNoDeploys) {
		err := fmt.Errorf("deploy journal is unreadable; GC skipped for %s (%s): %w", app, env, journalErr)
		summary.Failures = append(summary.Failures, err.Error())
		return summary, err
	}
	keep, protected, historyErr := gcKeepReleases(app, env, pointer.Release, entries)
	if historyErr != nil {
		err := fmt.Errorf("release history is unreadable; GC skipped for %s (%s): %w", app, env, historyErr)
		summary.Failures = append(summary.Failures, err.Error())
		return summary, err
	}
	for release := range keep {
		if release != pointer.Release {
			summary.KeptReleases = append(summary.KeptReleases, release)
		}
	}
	for release := range protected {
		if release != pointer.Release {
			summary.KeptReleases = append(summary.KeptReleases, release)
		}
	}
	sort.Strings(summary.KeptReleases[1:])

	artifactKeep := gcReleaseSetUnion(keep, protected)
	// Only the active activation's frozen env is ever read; rollback mints a
	// fresh activation and re-resolves secrets, so older activation env files
	// are unread copies of old secret values. Fresh prepare debris survives
	// via the grace period in gcRemoveActivations.
	keptActivations := map[string]bool{pointer.Activation: true}
	keptEnvelopeHashes := gcKeptEnvelopeHashes(pointer, entries, artifactKeep)
	gcRemoveContainers(app, env, &pointer, &summary)
	gcRemoveImages(app, env, keep, protected, &summary)
	gcRemoveStatic(app, env, artifactKeep, keptEnvelopeHashes, &summary)
	gcRemoveActivations(app, env, keptActivations, &summary)
	gcRemoveTempDirs(app, env, &summary)

	if len(summary.Removed) > 0 {
		journalSummary := strings.Join(summary.Removed, ", ")
		journalErr = appendDeployJournalEntry(app, env, deployJournalEntry{
			Outcome:          "gc",
			StartedAt:        gcNow().UTC().Format(time.RFC3339Nano),
			EndedAt:          gcNow().UTC().Format(time.RFC3339Nano),
			AttemptedRelease: pointer.Release,
			GC:               journalSummary,
			Identity:         deployActor("", ""),
		}, nil)
		if journalErr != nil {
			return summary, journalErr
		}
	}
	if len(summary.Failures) > 0 {
		return summary, fmt.Errorf("GC completed with %d cleanup failure(s)", len(summary.Failures))
	}
	return summary, nil
}

func gcKeepReleases(app, env, active string, entries []deployJournalEntry) (map[string]bool, map[string]bool, error) {
	keep := map[string]bool{active: true}
	protected := map[string]bool{}
	history, err := releaseDeployHistory(entries)
	if err != nil {
		return keep, protected, err
	}
	limit := releaseImageKeepLimit(env)
	verified := 0
	for _, record := range history {
		if record.Release == active {
			continue
		}
		if err := verifyGCRelease(app, env, record.Release); err != nil {
			protected[record.Release] = true
			continue
		}
		if verified < limit {
			keep[record.Release] = true
			verified++
		}
	}
	return keep, protected, nil
}

func gcReleaseSetUnion(sets ...map[string]bool) map[string]bool {
	union := map[string]bool{}
	for _, set := range sets {
		for release := range set {
			union[release] = true
		}
	}
	return union
}

func verifyGCRelease(app, env, release string) error {
	images, err := podmanImages(app, env)
	if err != nil {
		return err
	}
	byRelease := map[string]imageRelease{}
	for _, image := range images {
		if image.Release == release && image.Envelope.Schema != 0 {
			byRelease[release] = image
			break
		}
	}
	candidate := byRelease[release]
	if sidecar, sidecarErr := readStaticReleaseEnvelope(app, env, release); sidecarErr == nil {
		candidate = imageRelease{Release: release, Image: identity.ImageTag(app, env, release), Envelope: sidecar}
	}
	if candidate.Envelope.Schema == 0 {
		return fmt.Errorf("release %s image or static envelope is unavailable", release)
	}
	return verifyReleaseCandidate(app, env, candidate, byRelease)
}

func gcRemoveContainers(app, env string, pointer *activation.Pointer, summary *gcSummary) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		summary.Failures = append(summary.Failures, "containers: "+err.Error())
		return
	}
	for _, entry := range entries {
		if len(entry.Names) == 0 || entry.Labels["ship.app"] != app || entry.Labels["ship.env"] != env {
			continue
		}
		if entry.Labels["ship.release"] == pointer.Release && entry.Labels["ship.activation"] == pointer.Activation && entry.State == "running" {
			continue
		}
		name := entry.Names[0]
		if _, err := utils.RunChecked("podman", []string{"rm", "-f", name}, ""); err != nil {
			summary.Failures = append(summary.Failures, name+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "container "+name)
	}
}

func gcRemoveImages(app, env string, keep, protected map[string]bool, summary *gcSummary) {
	entries, err := podmanAllImagesForEnv(app, env)
	if err != nil {
		summary.Failures = append(summary.Failures, "images: "+err.Error())
		return
	}
	for _, image := range entries {
		if keep[image.Release] || protected[image.Release] {
			continue
		}
		if freshImage(image) {
			summary.Skipped = append(summary.Skipped, "image "+image.Image)
			continue
		}
		if err := runPodmanImagePruneCommand("rmi", image.Image); err != nil {
			summary.Failures = append(summary.Failures, "image "+image.Image+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "image "+image.Image)
	}
}

func podmanAllImagesForEnv(app, env string) ([]imageRelease, error) {
	out, err := utils.RunChecked("podman", []string{"images", "--format", "json"}, "")
	if err != nil {
		return nil, err
	}
	var entries []imageEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &entries); err != nil {
		return nil, fmt.Errorf("parse podman images json: %v", err)
	}
	return imageReleasesFromEntries(app, env, entries), nil
}

func freshImage(image imageRelease) bool {
	return !image.CreatedAt.IsZero() && gcNow().Sub(image.CreatedAt) < gcGracePeriod
}

func gcKeptEnvelopeHashes(pointer activation.Pointer, entries []deployJournalEntry, keep map[string]bool) map[string]bool {
	hashes := map[string]bool{pointer.Release + "\x00" + pointer.EnvelopeHash: true}
	for _, entry := range entries {
		if !keep[entry.AttemptedRelease] || entry.EnvelopeHash == "" {
			continue
		}
		hashes[entry.AttemptedRelease+"\x00"+entry.EnvelopeHash] = true
	}
	return hashes
}

func gcRemoveStatic(app, env string, keep map[string]bool, keepEnvelopeHashes map[string]bool, summary *gcSummary) {
	root := filepath.Join(identity.StaticDir(app, env), "releases")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		summary.Failures = append(summary.Failures, "static releases: "+err.Error())
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if keep[entry.Name()] {
			gcRemoveUnreferencedStaticSidecars(app, env, entry.Name(), keepEnvelopeHashes, summary)
			continue
		}
		if freshPath(path) {
			if !keep[entry.Name()] && freshPath(filepath.Join(root, entry.Name())) {
				summary.Skipped = append(summary.Skipped, "static "+filepath.Join(root, entry.Name()))
			}
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			summary.Failures = append(summary.Failures, "static "+path+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "static "+path)
	}
}

func gcRemoveUnreferencedStaticSidecars(app, env, release string, keep map[string]bool, summary *gcSummary) {
	root := filepath.Join(identity.StaticDir(app, env), "releases", release)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".ship-release-") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		e, decodeErr := envelope.DecodeJSON(data)
		if decodeErr != nil {
			continue
		}
		label, labelErr := e.LabelValue()
		if labelErr != nil || !keep[release+"\x00"+envelope.HashLabel(label)] {
			if err := os.Remove(path); err == nil {
				summary.Removed = append(summary.Removed, "static envelope "+path)
			}
		}
	}
}

func gcRemoveActivations(app, env string, keep map[string]bool, summary *gcSummary) {
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
		if keep[id] || freshPath(path) {
			if !keep[id] && freshPath(path) {
				summary.Skipped = append(summary.Skipped, "activation "+path)
			}
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
	roots := []string{identity.StaticDir(app, env), identity.EnvRoot(app, env)}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".") || (!strings.HasPrefix(entry.Name(), ".staging-") && !strings.HasPrefix(entry.Name(), ".data-") && !strings.HasPrefix(entry.Name(), ".ship-")) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			if freshPath(path) {
				summary.Skipped = append(summary.Skipped, "temp "+path)
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				summary.Failures = append(summary.Failures, "temp "+path+": "+err.Error())
				continue
			}
			summary.Removed = append(summary.Removed, "temp "+path)
		}
	}
}

func gcRemoveGlobalDeployTemps(summary *gcBoxSummary) {
	root := host.DeployTmpDir()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		summary.Failures = append(summary.Failures, "deploy temp directory: "+err.Error())
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if freshPathWithin(path, globalDeployTempGracePeriod) {
			summary.Skipped = append(summary.Skipped, "temp "+path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			summary.Failures = append(summary.Failures, "temp "+path+": "+err.Error())
			continue
		}
		summary.Removed = append(summary.Removed, "temp "+path)
	}
}

func freshPath(path string) bool {
	return freshPathWithin(path, gcGracePeriod)
}

func freshPathWithin(path string, grace time.Duration) bool {
	info, err := os.Stat(path)
	return err == nil && gcNow().Sub(info.ModTime()) < grace
}
