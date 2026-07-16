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
	Release  string
	Sequence int
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
	seen := map[string]bool{}
	var history []releaseDeployRecord
	for i := len(all) - 1; i >= 0; i-- {
		entry := all[i]
		if (entry.Outcome != "deployed" && entry.Outcome != "rolled_back") || entry.AttemptedRelease == "" {
			continue
		}
		if err := validateRelease(entry.AttemptedRelease); err != nil {
			return nil, err
		}
		if seen[entry.AttemptedRelease] {
			continue
		}
		seen[entry.AttemptedRelease] = true
		history = append(history, releaseDeployRecord{Release: entry.AttemptedRelease, Sequence: i})
	}
	return history, nil
}

func retainedReleaseHistory(env, active string, history []releaseDeployRecord) []releaseDeployRecord {
	retained := make([]releaseDeployRecord, 0, releaseImageKeepLimit(env)+1)
	for _, record := range history {
		if record.Release == active {
			retained = append(retained, record)
		}
	}
	kept := 0
	for _, record := range history {
		if record.Release == active {
			continue
		}
		if kept == releaseImageKeepLimit(env) {
			break
		}
		retained = append(retained, record)
		kept++
	}
	return retained
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
