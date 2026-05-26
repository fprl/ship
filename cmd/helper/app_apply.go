package helper

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/caddy"
	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/systemd"
	"github.com/fprl/simple-vps/internal/utils"
)

// appApplyCmd is the per-env, all-services deploy primitive. Given a
// streamed source tarball and a manifest, it:
//
//  1. Validates the manifest against the new shape rules.
//  2. Writes the resolved env file to <shared>/.env (literal values only
//     in this slice; @secret:KEY resolution against the per-(app, env,
//     key) store lands in a follow-up).
//  3. Extracts the tarball and runs `podman build` to tag a new image.
//  4. For each service: stops the running container, starts a fresh
//     one joined to the per-env network plus the shared `ingress`
//     network, waits for the healthcheck.
//  5. Synthesizes a Caddyfile fragment for the manifest's routes and
//     reloads Caddy.
//
// Atomicity in this slice is per-service (ADR-0006 Cut 1), but the
// blue/green rolling lifecycle is simplified to stop-old → start-new.
// Full -new/-old rename choreography lands in a follow-up.
type appApplyCmd struct {
	App      string `arg:"" help:"App name."`
	Env      string `arg:"" help:"Env name."`
	Tarball  string `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest string `name:"manifest" required:"" help:"Path to the uploaded simple-vps.toml."`
	SHA      string `name:"sha" required:"" help:"Release identifier (short git SHA or dirty-<timestamp>)."`
}

func (c appApplyCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	// systemd.ValidateDeployTmpSource resolves symlinks, ensures the
	// path is a regular file under the deploy tmp root, and (if invoked
	// via sudo) verifies the file is owned by the deploying user — so a
	// malicious local user can't leave a file behind for the helper to
	// pick up.
	tarball, err := systemd.ValidateDeployTmpSource(c.Tarball)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	manifestPath, err := systemd.ValidateDeployTmpSource(c.Manifest)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// Manifest sits in a temp dir created by the client; CheckManifest
	// reads the rest of the working tree it expects (Dockerfile) from
	// the SAME directory. So we extract the tarball alongside the
	// uploaded manifest into a context dir and run the validator there.
	ctxDir, err := os.MkdirTemp(systemd.DeployTmpDir(), "ctx-")
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	defer os.RemoveAll(ctxDir)

	if _, err := utils.RunChecked("tar", []string{"-xf", tarball, "-C", ctxDir}, ""); err != nil {
		utils.Die(fmt.Sprintf("extract tarball: %v", err), 1)
	}
	// The uploaded manifest is authoritative — overwrite any manifest
	// that might have been in the tarball.
	if _, err := utils.RunChecked("install", []string{"-m", "0644", manifestPath, filepath.Join(ctxDir, "simple-vps.toml")}, ""); err != nil {
		utils.Die(fmt.Sprintf("install manifest: %v", err), 1)
	}

	checkErrors, _, err := config.CheckManifest(ctxDir, c.Env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if len(checkErrors) > 0 {
		utils.Die(fmt.Sprintf("manifest invalid: %s", strings.Join(checkErrors, "; ")), 1)
	}
	app, err := config.LoadAppContext(ctxDir, c.Env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if app.Shape != config.ShapeContainer {
		utils.Die(fmt.Sprintf("apply currently supports container apps only (got shape %q); static apps land in a follow-up", app.Shape), 1)
	}
	if len(app.Services) == 0 {
		utils.Die("manifest must declare at least one [services.<name>] block", 1)
	}
	// @secret:KEY refs are recognized but resolution against the
	// per-(app, env, key) secret store hasn't landed yet. Refuse loudly
	// rather than silently dropping the variable from the env file.
	if len(app.SecretRefs) > 0 {
		var keys []string
		for k := range app.SecretRefs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		utils.Die(fmt.Sprintf("manifest references @secret:* values (%s) but secret resolution is not implemented yet; remove the refs or wait for the follow-up PR", strings.Join(keys, ", ")), 1)
	}

	// 2. Resolve env. Literal values only for now.
	if err := writeEnvFile(c.App, c.Env, app.Env); err != nil {
		utils.Die(err.Error(), 1)
	}

	// 3. Resolve numeric uid:gid for the per-env user. Podman's `--user`
	// resolves names inside the container's /etc/passwd, which generally
	// won't know about our host account. Pass numeric ids instead so the
	// container process runs as the host owner of /var/apps/<app>/<env>.
	userID, groupID, err := hostUserIDs(identity.SystemUser(c.App, c.Env))
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// 4. Allocate host ports for each port-having service.
	servicePorts, err := allocateServicePorts(c.App, c.Env, app.Services)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// 5. Build the image.
	imageTag := identity.ImageTag(c.App, c.Env, c.SHA)
	if _, err := utils.RunChecked("podman",
		[]string{"build",
			"-t", imageTag,
			"--label", "app=" + c.App,
			"--label", "env=" + c.Env,
			"--label", "simple_vps_release=" + c.SHA,
			"-f", filepath.Join(ctxDir, "Dockerfile"),
			ctxDir,
		}, "",
	); err != nil {
		utils.Die(fmt.Sprintf("podman build: %v", err), 1)
	}

	// 6. Start each service.
	for _, svcName := range sortedKeys(app.Services) {
		svc := app.Services[svcName]
		hostPort := servicePorts[svcName]
		if err := startService(c.App, c.Env, svcName, svc, imageTag, userID, groupID, hostPort); err != nil {
			utils.Die(err.Error(), 1)
		}
	}

	// 7. Write the per-app Caddyfile fragment using the allocated host
	// ports, then reload the live Caddy. The fragment lives under the
	// imported `conf.d/` directory; we never `caddy reload --config
	// <fragment>` directly because that would *replace* the active
	// config with just this app. We also validate the FULL Caddyfile
	// (with the new fragment in place) before reload, so a bad fragment
	// is caught before it breaks the live ingress.
	//
	// Snapshot the previous fragment first: if validate fails we must
	// restore it, not delete it. A previously-healthy app would
	// otherwise lose its route on the next reload from anywhere.
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		utils.Die(fmt.Sprintf("snapshot existing fragment: %v", err), 1)
	}
	if err := writeAppCaddyfile(c.App, c.Env, app, servicePorts); err != nil {
		utils.Die(err.Error(), 1)
	}
	if _, err := utils.RunChecked("caddy", []string{"validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			utils.Die(fmt.Sprintf("caddy validate rejected the fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr), 1)
		}
		utils.Die(fmt.Sprintf("caddy validate rejected the fragment, restored previous: %v", err), 1)
	}
	if _, err := utils.RunChecked("systemctl", []string{"reload", "caddy"}, ""); err != nil {
		utils.Die(fmt.Sprintf("systemctl reload caddy: %v", err), 1)
	}

	fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, c.SHA)
	return nil
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

const hostPortLabel = "simple_vps_host_port"

// hostPortRangeLow/High bound the per-(app, env, service) host port
// allocations. Documented in ADR-0005 §16 as `33000-33999`; the helper
// owns this allocator until Caddy-in-container makes it unnecessary.
const (
	hostPortRangeLow  = 33000
	hostPortRangeHigh = 34000
)

// allocateServicePorts assigns each port-having service a host port,
// reusing the existing allocation when a previous container of the same
// name already advertises one via the `simple_vps_host_port` label.
// Workers (no port) get 0 (sentinel "no port").
func allocateServicePorts(app, env string, services map[string]config.Service) (map[string]int, error) {
	out := make(map[string]int, len(services))
	used, err := globalUsedHostPorts()
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(services) {
		svc := services[name]
		if svc.Port == nil {
			continue
		}
		container := identity.ContainerName(app, env, name)
		if existing, ok := hostPortForContainer(container); ok {
			out[name] = existing
			used[existing] = true
			continue
		}
		port, err := pickHostPort(used, hostPortRangeLow, hostPortRangeHigh)
		if err != nil {
			return nil, fmt.Errorf("allocate host port for %s: %v", name, err)
		}
		out[name] = port
		used[port] = true
	}
	return out, nil
}

// pickHostPort returns the lowest free port in [low, high). Pure
// function for testability.
func pickHostPort(used map[int]bool, low, high int) (int, error) {
	for p := low; p < high; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free host port in [%d, %d)", low, high)
}

// hostPortForContainer reads the `simple_vps_host_port` label off an
// existing container, if any. Returns (0, false) when the container is
// absent or unlabelled.
func hostPortForContainer(name string) (int, bool) {
	out, err := utils.RunChecked("podman",
		[]string{"inspect", "--format", "{{ index .Config.Labels \"" + hostPortLabel + "\" }}", name},
		"",
	)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// globalUsedHostPorts returns every host port currently advertised by
// any simple-vps-managed container via the `simple_vps_host_port`
// label, so the allocator never collides across apps or envs.
func globalUsedHostPorts() (map[int]bool, error) {
	out, err := utils.RunChecked("podman",
		[]string{"ps", "-a", "--format", "{{ index .Labels \"" + hostPortLabel + "\" }}"},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("podman ps: %v", err)
	}
	used := make(map[int]bool)
	for _, line := range splitNonEmptyLines(string(out)) {
		if port, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && port > 0 {
			used[port] = true
		}
	}
	return used, nil
}

func renderEnvFile(vals map[string]string) string {
	var lines []string
	for _, k := range sortedKeys(vals) {
		lines = append(lines, fmt.Sprintf("%s=%s", k, vals[k]))
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	return content
}

func writeEnvFile(app, env string, vals map[string]string) error {
	path := identity.EnvFile(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(renderEnvFile(vals)), 0600); err != nil {
		return err
	}
	user := identity.SystemUser(app, env)
	if _, err := utils.RunChecked("chown", []string{user + ":" + user, path}, ""); err != nil {
		return fmt.Errorf("chown env file: %v", err)
	}
	return nil
}

func startService(app, env, svcName string, svc config.Service, imageTag, userID, groupID string, hostPort int) error {
	containerName := identity.ContainerName(app, env, svcName)
	envFile := identity.EnvFile(app, env)
	shared := identity.SharedDir(app, env)
	appNet := identity.Network(app, env)

	// Stop and remove any existing container of the same name.
	_, _ = utils.RunChecked("podman", []string{"rm", "-f", containerName}, "")

	// Initial hardening subset from ADR-0005 §7: user/cap-drop/
	// no-new-privileges/pids-limit/per-env network, plus read-only
	// rootfs with a small writable tmpfs at /tmp. Manifest-driven
	// `--memory` and `--cpus` limits, and `--cap-add=NET_BIND_SERVICE`
	// for services binding <1024, require schema additions and land in
	// a follow-up PR.
	args := []string{
		"run", "-d",
		"--name", containerName,
		"--restart", "unless-stopped",
		"--user", userID + ":" + groupID,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--read-only",
		"--tmpfs", "/tmp:size=64m",
		"--network", appNet,
		"--network", "ingress",
		"-v", shared + ":" + shared + ":Z",
		"--label", "app=" + app,
		"--label", "env=" + env,
		"--label", "service=" + svcName,
	}
	if _, err := os.Stat(envFile); err == nil {
		args = append(args, "--env-file", envFile)
	}
	if svc.Port != nil && hostPort > 0 {
		// Host-loopback publish via the allocated port so Caddy on the
		// host can reverse-proxy without joining Podman's network plane
		// (ADR-0005 §16). The allocation is recorded on the container
		// itself via the `simple_vps_host_port` label so the next deploy
		// can reuse the same port. The `ingress` network drops out of
		// the routing path until Caddy-in-container lands.
		args = append(args,
			"--publish", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, *svc.Port),
			"--label", hostPortLabel+"="+strconv.Itoa(hostPort),
		)
	}
	args = append(args, imageTag)
	if svc.Command != "" {
		// Override the image CMD via /bin/sh -c so users can write the
		// command as a single string (ADR-0005 §13).
		args = append(args, "/bin/sh", "-c", svc.Command)
	}

	if _, err := utils.RunChecked("podman", args, ""); err != nil {
		return fmt.Errorf("podman run %s: %v", containerName, err)
	}

	if svc.Port != nil && hostPort > 0 && svc.Healthcheck != "" {
		if err := waitHealthy(hostPort, svc.Healthcheck, healthcheckTimeout(svc)); err != nil {
			// Surface logs on failure so the user can see why.
			out, _ := exec.Command("podman", "logs", "--tail", "50", containerName).CombinedOutput()
			os.Stderr.Write(out)
			return fmt.Errorf("healthcheck failed for %s: %v", svcName, err)
		}
	}
	return nil
}

func healthcheckTimeout(svc config.Service) time.Duration {
	if svc.HealthcheckTimeout != nil && *svc.HealthcheckTimeout > 0 {
		return time.Duration(*svc.HealthcheckTimeout) * time.Second
	}
	return 30 * time.Second
}

func waitHealthy(port int, path string, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %s", timeout)
	}
	return lastErr
}

func caddyfilePath(app, env string) string {
	return fmt.Sprintf("/etc/caddy/conf.d/simple-vps-%s-%s.caddy", app, env)
}

func renderAppCaddyfile(ctx *config.AppContext, servicePorts map[string]int) (string, error) {
	var blocks []string
	for _, name := range sortedKeys(ctx.Routes) {
		route := ctx.Routes[name]
		var body string
		switch route.Type {
		case "proxy":
			svc, ok := ctx.Services[route.Service]
			if !ok || svc.Port == nil {
				return "", fmt.Errorf("route %q references service %q with no port", name, route.Service)
			}
			hostPort := servicePorts[route.Service]
			if hostPort <= 0 {
				return "", fmt.Errorf("route %q: no host port allocated for service %q", name, route.Service)
			}
			body = fmt.Sprintf("\treverse_proxy 127.0.0.1:%d\n", hostPort)
		case "redirect":
			// Quote the destination so a hostile value can't inject extra
			// Caddyfile directives. Manifest validation only enforces an
			// http://‌/https:// prefix, which still leaves room for
			// whitespace/newline injection on the bare token form.
			quotedTo, err := caddy.CaddyQuote(route.To)
			if err != nil {
				return "", fmt.Errorf("route %q: %v", name, err)
			}
			body = fmt.Sprintf("\tredir %s permanent\n", quotedTo)
		case "static":
			// Static apps not in scope for this verb; skip.
			continue
		default:
			return "", fmt.Errorf("route %q: unsupported type %q", name, route.Type)
		}
		// Quote the host on the block selector for the same reason —
		// hostnames are validated, but `CaddyQuote` is the consistent
		// boundary for every user-controlled string we put into a
		// Caddyfile.
		quotedHost, err := caddy.CaddyQuote(route.Host)
		if err != nil {
			return "", fmt.Errorf("route %q: %v", name, err)
		}
		blocks = append(blocks, fmt.Sprintf("%s {\n%s}\n", quotedHost, body))
	}
	return "# generated by simple-vps server app apply — do not edit\n" + strings.Join(blocks, "\n"), nil
}

func writeAppCaddyfile(app, env string, ctx *config.AppContext, servicePorts map[string]int) error {
	content, err := renderAppCaddyfile(ctx, servicePorts)
	if err != nil {
		return err
	}
	path := caddyfilePath(app, env)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// snapshotCaddyFragment reads the existing fragment so a failed deploy
// can restore it. Returns (contents, true, nil) when a fragment exists,
// (nil, false, nil) when nothing is there, or an error for anything
// other than ENOENT.
func snapshotCaddyFragment(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

// restoreCaddyFragment puts back what snapshotCaddyFragment captured.
// If there was no previous fragment, remove the new one so subsequent
// reloads don't trip on it.
func restoreCaddyFragment(path string, prev []byte, existed bool) error {
	if existed {
		return os.WriteFile(path, prev, 0644)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
