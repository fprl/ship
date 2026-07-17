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
