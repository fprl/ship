package helper

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
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

func releaseDeployHistory(entries []deployJournalEntry, current *deployJournalEntry) ([]releaseDeployRecord, error) {
	all := append([]deployJournalEntry(nil), entries...)
	if current != nil {
		all = append(all, *current)
	}
	byRelease := map[string]releaseDeployRecord{}
	for i, entry := range all {
		if (entry.Outcome != "deployed" && entry.Outcome != "rolled_back") || entry.AttemptedRelease == "" {
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
