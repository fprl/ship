package helper

import (
	"fmt"
	"os"
)

func currentRelease(processes []processStatus) (string, error) {
	if len(processes) == 0 {
		return "", fmt.Errorf("no processes running; deploy before rollback")
	}
	release := processes[0].Release
	if release == "" {
		return "", fmt.Errorf("running processes do not expose a release label")
	}
	for _, process := range processes[1:] {
		if process.Release != release {
			return "", fmt.Errorf("running processes are on different releases")
		}
	}
	return release, nil
}

func currentStaticRelease(app, env string) (string, error) {
	pointer, err := readActive(app, env)
	if err != nil {
		return "", fmt.Errorf("active static release not found; deploy before rollback or backup")
	}
	if pointer.IsLegacy() || pointer.Artifact.StaticHash == "" {
		return "", fmt.Errorf("active static release is unavailable")
	}
	path := staticReleasePath(app, env, pointer.Artifact.Release, pointer.Artifact.StaticHash)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("static release %s not found: %v", pointer.Artifact.DisplayIdentity(), err)
	}
	return pointer.Artifact.Release, nil
}
