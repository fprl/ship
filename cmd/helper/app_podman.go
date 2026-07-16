package helper

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

const (
	previewDefaultMemory = "512m"
	previewDefaultCPUs   = 0.5
)

type podmanBaseRunOptions struct {
	App         string
	Env         string
	ProcessName string
	UserID      string
	GroupID     string
	Release     string
	Networks    []string
}

func podmanBuildArgs(app, env, imageTag, release, dockerfile, ctxDir string, rebuild bool) []string {
	return podmanBuildArgsWithEnvelope(app, env, imageTag, release, dockerfile, ctxDir, rebuild, "")
}

func podmanBuildArgsWithEnvelope(app, env, imageTag, release, dockerfile, ctxDir string, rebuild bool, envelopeLabel string) []string {
	args := []string{"build"}
	if rebuild {
		args = append(args, "--no-cache", "--pull=always")
	}
	args = append(args,
		"-t", imageTag,
		"--label", "ship.app="+app,
		"--label", "ship.env="+env,
		"--label", "ship.infra_id="+identity.InfraID(app, env),
		"--label", "ship.release="+release,
	)
	if envelopeLabel != "" {
		args = append(args, "--label", "ship.release_envelope="+envelopeLabel)
	}
	args = append(args, "-f", dockerfile, ctxDir)
	return args
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
	if uid == "" || gid == "" {
		return "", "", fmt.Errorf("empty id output for %s", name)
	}
	return uid, gid, nil
}

func podmanBaseRunArgs(opts podmanBaseRunOptions) []string {
	args := []string{
		"--user", opts.UserID + ":" + opts.GroupID,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
	}
	for _, network := range opts.Networks {
		args = append(args, "--network", network)
	}
	args = append(args,
		"-v", identity.DataDir(opts.App, opts.Env)+":/data:Z",
		"--label", "ship.app="+opts.App,
		"--label", "ship.env="+opts.Env,
		"--label", "ship.process="+opts.ProcessName,
		"--label", "ship.infra_id="+identity.InfraID(opts.App, opts.Env),
		"--label", "ship.release="+opts.Release,
	)
	return args
}

func appendReadOnlyRuntimeArgs(args []string) []string {
	return append(args,
		"--read-only",
		// mode=1777 (sticky world-writable) so the per-env container
		// user (--user above) can actually write here. Without it,
		// the tmpfs is owned by root and the unprivileged container
		// process fails with EACCES.
		"--tmpfs", "/tmp:size=64m,mode=1777",
	)
}

func appendResourceArgs(args []string, resources config.Resources) []string {
	if resources.Memory != nil {
		args = append(args, "--memory", *resources.Memory)
	}
	if resources.CPUs != nil {
		args = append(args, "--cpus", strconv.FormatFloat(*resources.CPUs, 'f', -1, 64))
	}
	return args
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
func buildPodmanRunArgs(app, env, processName string, proc config.Process, imageTag, userID, groupID, release, containerName string, envFileExists bool, previewEnv bool) []string {
	envFile, _ := activeEnvFile(app, env)
	return buildPodmanRunArgsWithEnvFile(app, env, processName, proc, imageTag, userID, groupID, release, containerName, envFileExists, previewEnv, envFile)
}

func buildPodmanRunArgsWithEnvFile(app, env, processName string, proc config.Process, imageTag, userID, groupID, release, containerName string, envFileExists bool, previewEnv bool, envFile string) []string {
	appNet := identity.Network(app, env)
	resources := effectiveProcessResources(proc, previewEnv)

	args := []string{
		"run", "-d",
		"--name", containerName,
		// Long-running app processes should come back after host or
		// Podman restarts. Release and exec containers are one-shot and
		// intentionally do not set a restart policy.
		"--restart", "unless-stopped",
	}
	args = append(args, podmanBaseRunArgs(podmanBaseRunOptions{
		App:         app,
		Env:         env,
		ProcessName: processName,
		UserID:      userID,
		GroupID:     groupID,
		Release:     release,
		// App processes join ingress so Caddy can reach them by
		// container DNS. Release and exec commands stay off ingress.
		Networks: []string{appNet, "ingress"},
	})...)
	// App processes and exec commands keep a read-only rootfs with a
	// writable /tmp. Release commands preserve today's looser rootfs
	// behavior for migrations that write inside image-provided paths.
	args = appendReadOnlyRuntimeArgs(args)
	if proc.Port != nil {
		args = append(args, "--label", "ship.port="+strconv.Itoa(*proc.Port))
	}
	args = appendResourceArgs(args, resources)
	if envFileExists && envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, imageTag)
	if proc.Command != "" {
		// Override the image CMD via /bin/sh -c so users can write the
		// command as a single string (ADR-0005 §13).
		args = append(args, "/bin/sh", "-c", proc.Command)
	}
	return args
}

func effectiveProcessResources(proc config.Process, previewEnv bool) config.Resources {
	resources := proc.Resources
	if !previewEnv {
		return resources
	}
	if resources.Memory == nil {
		memory := previewDefaultMemory
		resources.Memory = &memory
	}
	if resources.CPUs == nil {
		cpus := previewDefaultCPUs
		resources.CPUs = &cpus
	}
	return resources
}

func startProcess(app, env, processName string, proc config.Process, imageTag, userID, groupID, release, containerName string, probe string, previewEnv bool, scrubValues []string, envFile string) error {

	_, _ = utils.RunChecked("podman", []string{"rm", "-f", containerName}, "")

	envFileExists := false
	if _, err := os.Stat(envFile); err == nil {
		envFileExists = true
	}
	args := buildPodmanRunArgsWithEnvFile(app, env, processName, proc, imageTag, userID, groupID, release, containerName, envFileExists, previewEnv, envFile)

	if _, err := utils.RunChecked("podman", args, ""); err != nil {
		return fmt.Errorf("podman run %s: %v", containerName, err)
	}

	if proc.Port != nil && probe != "" {
		if err := waitHealthy(containerName, *proc.Port, probe, 30*time.Second); err != nil {
			// Surface logs on failure so the user can see why.
			out, _ := exec.Command("podman", "logs", "--tail", "50", containerName).CombinedOutput()
			_, _ = os.Stderr.Write([]byte(scrubText(string(out), scrubValues)))
			return fmt.Errorf("health check failed for %s: %w", processName, err)
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

func runReleaseCommand(app, env, command, imageTag, userID, groupID, release, envFile string) error {
	name := identity.ContainerName(app, env, "release", release)
	_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
	args := []string{
		"run", "--rm",
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
		Networks:    []string{identity.Network(app, env)},
	})...)
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, imageTag, "/bin/sh", "-c", command)
	if _, err := utils.RunCheckedWithTimeout("podman", args, "", releaseCommandTimeout); err != nil {
		_, _ = utils.RunChecked("podman", []string{"rm", "-f", name}, "")
		return fmt.Errorf("release command %q failed before traffic switch: %w", command, err)
	}
	return nil
}

func processContainers(entries []containerEntry, processName, excludeRelease string) []string {
	var names []string
	for _, e := range entries {
		if e.Labels["ship.process"] != processName {
			continue
		}
		if excludeRelease != "" && e.Labels["ship.release"] == excludeRelease {
			continue
		}
		if len(e.Names) > 0 {
			names = append(names, e.Names[0])
		}
	}
	sort.Strings(names)
	return names
}
