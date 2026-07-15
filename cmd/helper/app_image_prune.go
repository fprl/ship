package helper

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
)

const (
	productionReleaseImageKeep = 5
	previewReleaseImageKeep    = 2
	imagePruneCommandTimeout   = 15 * time.Second
)

type releaseDeployRecord struct {
	Release    string
	DeployedAt time.Time
	Sequence   int
}

func releaseImageKeepLimit(env string) int {
	if env == productionEnvName {
		return productionReleaseImageKeep
	}
	return previewReleaseImageKeep
}

func bestEffortPruneReleaseImagesAfterDeploy(app, env, liveFallback string, current deployJournalEntry) string {
	summary, err := pruneReleaseImagesAfterDeploy(app, env, liveFallback, current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "image prune failed for %s (%s): %s\n", app, env, singleLine(err.Error()))
	}
	return summary
}

func pruneReleaseImagesAfterDeploy(app, env, liveFallback string, current deployJournalEntry) (string, error) {
	images, err := podmanImages(app, env)
	if err != nil {
		return "", err
	}
	entries, torn, err := readDeployJournalEntriesWithStatus(app, env)
	if err != nil {
		if !errcat.Is(err, errcat.CodeNoDeploys) {
			return "", err
		}
		entries = nil
	}
	if torn {
		warnTornDeployJournal(identity.DeployJournalFile(app, env))
	}
	liveRelease := currentActiveReleaseBestEffort(app, env)
	if liveRelease == "" {
		liveRelease = liveFallback
	}
	if liveRelease == "" && len(images) > 0 {
		return "", fmt.Errorf("cannot determine live release")
	}
	keep, err := releaseImageKeepSet(entries, &current, liveRelease, releaseImageKeepLimit(env))
	if err != nil {
		return "", err
	}
	removed, pruneErr := pruneReleaseImages(images, keep)
	if danglingErr := pruneDanglingImages(); danglingErr != nil && pruneErr == nil {
		pruneErr = danglingErr
	}
	return imagePruneSummary(removed), pruneErr
}

func purgeReleaseImagesForEnv(app, env string) (int, error) {
	images, err := podmanImages(app, env)
	if err != nil {
		return 0, err
	}
	removed, pruneErr := pruneReleaseImages(images, nil)
	if danglingErr := pruneDanglingImages(); danglingErr != nil && pruneErr == nil {
		pruneErr = danglingErr
	}
	return removed, pruneErr
}

func releaseImageKeepSet(entries []deployJournalEntry, current *deployJournalEntry, liveRelease string, limit int) (map[string]bool, error) {
	history, err := releaseDeployHistory(entries, current)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for i, record := range history {
		if i >= limit {
			break
		}
		keep[record.Release] = true
	}
	if liveRelease != "" {
		if err := validateRelease(liveRelease); err != nil {
			return nil, err
		}
		keep[liveRelease] = true
	}
	return keep, nil
}

func releaseDeployHistory(entries []deployJournalEntry, current *deployJournalEntry) ([]releaseDeployRecord, error) {
	all := append([]deployJournalEntry(nil), entries...)
	if current != nil {
		all = append(all, *current)
	}
	byRelease := map[string]releaseDeployRecord{}
	for i, entry := range all {
		if entry.Outcome != "deployed" || entry.AttemptedRelease == "" {
			continue
		}
		if err := validateRelease(entry.AttemptedRelease); err != nil {
			return nil, err
		}
		at, err := journalDeployTime(entry)
		if err != nil {
			return nil, err
		}
		record := releaseDeployRecord{Release: entry.AttemptedRelease, DeployedAt: at, Sequence: i}
		if existing, ok := byRelease[record.Release]; !ok || deployRecordAfter(record, existing) {
			byRelease[record.Release] = record
		}
	}
	history := make([]releaseDeployRecord, 0, len(byRelease))
	for _, record := range byRelease {
		history = append(history, record)
	}
	sort.Slice(history, func(i, j int) bool {
		return deployRecordAfter(history[i], history[j])
	})
	return history, nil
}

func journalDeployTime(entry deployJournalEntry) (time.Time, error) {
	value := entry.EndedAt
	if value == "" {
		value = entry.StartedAt
	}
	if value == "" {
		return time.Time{}, fmt.Errorf("deploy journal entry for release %s has no timestamp", entry.AttemptedRelease)
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("deploy journal entry for release %s has invalid timestamp %q: %v", entry.AttemptedRelease, value, err)
	}
	return at, nil
}

func deployRecordAfter(a, b releaseDeployRecord) bool {
	if !a.DeployedAt.Equal(b.DeployedAt) {
		return a.DeployedAt.After(b.DeployedAt)
	}
	if a.Sequence != b.Sequence {
		return a.Sequence > b.Sequence
	}
	return a.Release > b.Release
}

func pruneReleaseImages(images []imageRelease, keep map[string]bool) (int, error) {
	sort.Slice(images, func(i, j int) bool {
		return images[i].Release < images[j].Release
	})
	removed := 0
	var firstErr error
	for _, image := range images {
		if keep != nil && keep[image.Release] {
			continue
		}
		if err := runPodmanImagePruneCommand("rmi", image.Image); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func pruneDanglingImages() error {
	return runPodmanImagePruneCommand("image", "prune", "-f")
}

func runPodmanImagePruneCommand(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), imagePruneCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("podman %s timed out after %s", strings.Join(args, " "), imagePruneCommandTimeout)
	}
	if err != nil {
		if detail := singleLine(string(out)); detail != "" {
			return fmt.Errorf("podman %s: %s", strings.Join(args, " "), detail)
		}
		return fmt.Errorf("podman %s: %v", strings.Join(args, " "), err)
	}
	return nil
}

func imagePruneSummary(removed int) string {
	switch removed {
	case 0:
		return "pruned 0 old images"
	case 1:
		return "pruned 1 old image"
	default:
		return fmt.Sprintf("pruned %d old images", removed)
	}
}
