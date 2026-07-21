package helper

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/podmanruntime"
	"github.com/fprl/ship/internal/utils"
)

type podmanBaseRunOptions struct {
	App, Env, ProcessName, UserID, GroupID, Release, Activation string
	Networks                                                    []string
}

func podmanBuildArgs(app, env, imageTag, release, dockerfile, ctxDir string, rebuild bool) []string {
	return podmanBuildArgsWithEnvelope(app, env, imageTag, release, dockerfile, ctxDir, rebuild, "")
}

func podmanBuildArgsWithEnvelope(app, env, imageTag, release, dockerfile, ctxDir string, rebuild bool, envelopeLabel string) []string {
	return podmanruntime.BuildArgs(podmanruntime.BuildSpec{App: app, Env: env, ImageTag: imageTag, Release: release, Dockerfile: dockerfile, ContextDir: ctxDir, Rebuild: rebuild, EnvelopeLabel: envelopeLabel})
}

// hostUserIDs looks up the uid:gid for the per-env Linux account. We
// pass these numerically to podman so `--user` doesn't try to resolve
// the name inside the container image.
func hostUserIDs(name string) (string, string, error) {
	uidOut, err := utils.RunChecked("id", []string{"-u", name}, "")
	if err != nil {
		return "", "", fmt.Errorf("looking up uid for %s: %v", name, err)
	}
	gidOut, err := utils.RunChecked("id", []string{"-g", name}, "")
	if err != nil {
		return "", "", fmt.Errorf("looking up gid for %s: %v", name, err)
	}
	uid := strings.TrimSpace(string(uidOut))
	gid := strings.TrimSpace(string(gidOut))
	return uid, gid, nil
}

func podmanBaseRunArgs(opts podmanBaseRunOptions) []string {
	return podmanruntime.BaseRunArgs(podmanruntime.ContainerSpec{App: opts.App, Env: opts.Env, Process: opts.ProcessName, UserID: opts.UserID, GroupID: opts.GroupID, Release: opts.Release, Activation: opts.Activation, Networks: opts.Networks})
}

func appendReadOnlyRuntimeArgs(args []string) []string {
	return podmanruntime.WithReadOnlyRoot(args)
}

func appendResourceArgs(args []string, resources config.Resources) []string {
	return podmanruntime.WithResources(args, resources)
}

// buildPodmanRunArgs is the pure-function core of startProcess:
// produces the `podman run` argv for one process. Extracted so it can
// be unit-tested without shelling out.
//
// The initial hardening subset from ADR-0005 §7 is always present:
// per-env Linux user, --cap-drop=ALL, --security-opt no-new-privileges,
// --pids-limit, --read-only with a default 64 MiB tmpfs at /tmp.
// No --publish: Caddy reaches the process over the shared `ingress`
// network by container DNS. Manifest-declared memory and CPU limits
// render to the closed set of runtime flags.
func buildPodmanRunArgsWithActivation(app, env, processName string, proc config.Process, imageTag, userID, groupID, release, activation, containerName string, envFileExists bool, previewEnv bool, envFile string) []string {
	if !envFileExists {
		envFile = ""
	}
	return podmanruntime.ProcessArgs(podmanruntime.ProcessSpec{App: app, Env: env, Process: processName, Definition: proc, Image: imageTag, UserID: userID, GroupID: groupID, Release: release, Activation: activation, Container: containerName, EnvFile: envFile, Preview: previewEnv})
}

func effectiveProcessResources(proc config.Process, previewEnv bool) config.Resources {
	return podmanruntime.EffectiveResources(proc.Resources, previewEnv)
}

func startProcessWithActivation(app, env, processName string, proc config.Process, imageTag, userID, groupID, release, activation, containerName string, probe string, previewEnv bool, scrubValues []string, envFile string, progress *deployProgressEmitter) error {
	spec := podmanruntime.ProcessSpec{App: app, Env: env, Process: processName, Definition: proc, Image: imageTag, UserID: userID, GroupID: groupID, Release: release, Activation: activation, Container: containerName, EnvFile: envFile, Preview: previewEnv}
	finishStart := progress.start("start-"+processName, "Start "+processName)
	startErr := podmanruntime.CLI().StartProcess(spec)
	finishStart(startErr)
	if startErr != nil {
		return startErr
	}

	if proc.Port != nil && probe != "" {
		finishProbe := progress.start("probe-"+processName, "Probe "+processName+" · GET "+probe)
		probeErr := waitHealthy(containerName, *proc.Port, probe, 30*time.Second)
		finishProbe(probeErr)
		if probeErr != nil {
			// Surface logs on failure so the user can see why.
			out, _ := exec.Command("podman", "logs", "--tail", "50", containerName).CombinedOutput()
			_, _ = os.Stderr.Write([]byte(scrubText(string(out), scrubValues)))
			return fmt.Errorf("health check failed for %s: %w", processName, probeErr)
		}
	}
	return nil
}

type probeFailureError struct {
	Status      int
	BodySnippet string
	Detail      string
}

func (e *probeFailureError) Error() string {
	return e.Detail
}

// waitHealthy probes the app container's health path via Caddy on the
// shared `ingress` network. The helper itself runs on the host and is
// not a member of `ingress`, so it can't talk to the app container's
// DNS name directly. The Caddy container is on `ingress` by design and
// ships busybox `wget` — exec the probe inside it.
func waitHealthy(containerName string, port int, path string, timeout time.Duration) error {
	url := fmt.Sprintf("http://%s:%d%s", containerName, port, path)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := exec.Command("podman", "exec", "caddy", "wget", "-q", "-O", "-", "--timeout=2", url)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		lastErr = &probeFailureError{
			Status:      probeStatusFromDetail(detail),
			BodySnippet: tailLines(detail, 8),
			Detail:      fmt.Sprintf("%s: %s", url, detail),
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = &probeFailureError{Detail: fmt.Sprintf("timed out after %s", timeout)}
	}
	return lastErr
}

func probeStatusFromDetail(detail string) int {
	for _, field := range strings.Fields(detail) {
		field = strings.Trim(field, ".,:;()[]")
		code, err := strconv.Atoi(field)
		if err == nil && code >= 100 && code <= 599 {
			return code
		}
	}
	return 0
}

const releaseCommandTimeout = 10 * time.Minute

func runReleaseCommandWithActivation(app, env, command, imageTag, userID, groupID, release, activation, envFile string, progress *deployProgressEmitter, scrubValues []string) error {
	name := identity.ContainerName(app, env, "release", release)
	_ = podmanruntime.CLI().RemoveContainer(name)
	args := []string{
		"run", "--replace", "--rm",
		"--name", name,
	}
	// Release commands are one-shot migrations: --rm cleans them up,
	// no --restart is set, and they only join the app network because
	// Caddy never proxies to them.
	args = append(args, podmanBaseRunArgs(podmanBaseRunOptions{
		App:         app,
		Env:         env,
		ProcessName: "release",
		UserID:      userID,
		GroupID:     groupID,
		Release:     release,
		Activation:  activation,
		Networks:    []string{identity.Network(app, env)},
	})...)
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, imageTag, "/bin/sh", "-c", command)
	if _, err := runDeployCommand(progress, "release", scrubValues, releaseCommandTimeout, "podman", args, ""); err != nil {
		_ = podmanruntime.CLI().RemoveContainer(name)
		return fmt.Errorf("release command %q failed before traffic switch: %w", command, err)
	}
	return nil
}
