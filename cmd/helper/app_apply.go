package helper

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/caddy"
	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/utils"
)

// appApplyCmd is the per-env, all-services deploy primitive. Given a
// streamed source tarball and a manifest, it:
//
//  1. Validates the manifest against the new shape rules.
//  2. Writes the resolved env file to <shared>/.env (literal values only
//     plus @secret:KEY values resolved against the per-(app, env, key)
//     store).
//  3. Extracts the tarball and runs `podman build` to tag a new image.
//  4. For each service: stops the running container, starts a fresh
//     one joined to the per-env network plus the shared `ingress`
//     network, waits for the healthcheck.
//  5. Synthesizes a Caddyfile fragment for the manifest's routes and
//     reloads Caddy.
//
// Atomicity is per-service (ADR-0006 Cut 1). The current lifecycle is
// deliberately simple: stop the old container, start the replacement,
// verify it, then reload Caddy.
type appApplyCmd struct {
	App      string `arg:"" help:"App name."`
	Env      string `arg:"" help:"Env name."`
	Tarball  string `name:"tarball" required:"" help:"Path to the streamed source tarball."`
	Manifest string `name:"manifest" required:"" help:"Path to the uploaded simple-vps.toml."`
	SHA      string `name:"sha" required:"" help:"Release identifier (short git SHA or dirty-<timestamp>)."`
	Rebuild  bool   `name:"rebuild" help:"Pass --no-cache --pull=always to podman build."`
}

func (c appApplyCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	withAppEnvLock(c.App, c.Env, func() {
		c.runLocked()
	})
	return nil
}

func (c appApplyCmd) runLocked() {
	// host.ValidateDeployTmpSource resolves symlinks, ensures the
	// path is a regular file under the deploy tmp root, and (if invoked
	// via sudo) verifies the file is owned by the deploying user — so a
	// malicious local user can't leave a file behind for the helper to
	// pick up.
	tarball, err := host.ValidateDeployTmpSource(c.Tarball)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	manifestPath, err := host.ValidateDeployTmpSource(c.Manifest)
	if err != nil {
		utils.Die(err.Error(), 1)
	}

	// Manifest sits in a temp dir created by the client; CheckManifest
	// reads the rest of the working tree it expects (Dockerfile) from
	// the SAME directory. So we extract the tarball alongside the
	// uploaded manifest into a context dir and run the validator there.
	ctxDir, err := os.MkdirTemp(host.DeployTmpDir(), "ctx-")
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
	// 2. Resolve env. Literal values from the manifest first; then
	// pull each @secret:KEY reference from the per-(app, env, key)
	// store and merge into the same map. A missing secret fails the
	// deploy before any container restarts so we never half-apply.
	resolved, err := resolveEnv(c.App, c.Env, app.Env, app.SecretRefs)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	if err := writeEnvFile(c.App, c.Env, resolved); err != nil {
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

	// 4. Build the image.
	imageTag := identity.ImageTag(c.App, c.Env, c.SHA)
	buildArgs := podmanBuildArgs(c.App, c.Env, imageTag, c.SHA, filepath.Join(ctxDir, "Dockerfile"), ctxDir, c.Rebuild)
	if _, err := utils.RunChecked("podman", buildArgs, ""); err != nil {
		utils.Die(fmt.Sprintf("podman build: %v", err), 1)
	}

	// 5. Start each service. Containers join the per-(app, env) network
	// for intra-app DNS and the shared `ingress` network so Caddy can
	// reach them by container name (ADR-0006 Cut 2). No host-loopback
	// `--publish`: the host-port allocator that lived here is gone.
	for _, svcName := range sortedKeys(app.Services) {
		svc := app.Services[svcName]
		if err := startService(c.App, c.Env, svcName, svc, imageTag, userID, groupID); err != nil {
			utils.Die(err.Error(), 1)
		}
	}

	// 6. Write the per-app Caddyfile fragment (`reverse_proxy
	// http://<container>:<service-port>`), validate the full Caddyfile
	// inside the Caddy container, then reload Caddy in place. The
	// fragment lives under `/etc/caddy/conf.d/` which the main Caddyfile
	// imports; we never `caddy reload --config <fragment>` because that
	// would *replace* the active config with just this app.
	//
	// Snapshot the previous fragment first: if validate rejects the new
	// one we restore the old. A previously-healthy app would otherwise
	// lose its route on the next reload from anywhere.
	caddyPath := caddyfilePath(c.App, c.Env)
	prevFragment, prevExisted, err := snapshotCaddyFragment(caddyPath)
	if err != nil {
		utils.Die(fmt.Sprintf("snapshot existing fragment: %v", err), 1)
	}
	if err := writeAppCaddyfile(c.App, c.Env, app); err != nil {
		utils.Die(err.Error(), 1)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, ""); err != nil {
		if restoreErr := restoreCaddyFragment(caddyPath, prevFragment, prevExisted); restoreErr != nil {
			utils.Die(fmt.Sprintf("caddy validate rejected the fragment AND restore failed (manual fix required at %s): %v (restore: %v)", caddyPath, err, restoreErr), 1)
		}
		utils.Die(fmt.Sprintf("caddy validate rejected the fragment, restored previous: %v", err), 1)
	}
	if _, err := utils.RunChecked("podman", []string{"exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile"}, ""); err != nil {
		utils.Die(fmt.Sprintf("caddy reload: %v", err), 1)
	}

	fmt.Printf("Deployed %s (%s) at %s\n", c.App, c.Env, c.SHA)
}

func podmanBuildArgs(app, env, imageTag, release, dockerfile, ctxDir string, rebuild bool) []string {
	args := []string{"build"}
	if rebuild {
		args = append(args, "--no-cache", "--pull=always")
	}
	args = append(args,
		"-t", imageTag,
		"--label", "app="+app,
		"--label", "env="+env,
		"--label", "simple_vps_release="+release,
		"-f", dockerfile,
		ctxDir,
	)
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

// resolveEnv merges literal manifest env values with the runtime
// values pulled from the per-(app, env, key) secret store. A missing
// secret fails the whole resolution — no half-applied env file
// reaches the container, and no manifest-vs-store conflict is
// silently chosen for the user.
//
// Manifest literals and secret refs are guaranteed disjoint by
// `config.splitEnvBlock` (a value either *is* a secret ref or is a
// literal; never both). Returning a fresh map keeps the caller's
// `app.Env` intact for any future reuse.
func resolveEnv(app, env string, literals map[string]string, refs map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(literals)+len(refs))
	for k, v := range literals {
		out[k] = v
	}
	// Sorted for deterministic error messages when multiple refs miss.
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var missing []string
	for _, envKey := range keys {
		secretKey := refs[envKey]
		val, err := secrets.Get(app, env, secretKey)
		if errors.Is(err, secrets.ErrNotFound) {
			missing = append(missing, fmt.Sprintf("%s (looks up @secret:%s)", envKey, secretKey))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read secret %s: %w", secretKey, err)
		}
		out[envKey] = string(val)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unresolved @secret references: %s — run `simple-vps secret put %s <key>` for each", strings.Join(missing, ", "), env)
	}
	return out, nil
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

// buildPodmanRunArgs is the pure-function core of startService:
// produces the `podman run` argv for one service. Extracted so it can
// be unit-tested without shelling out.
//
// The initial hardening subset from ADR-0005 §7 is always present:
// per-env Linux user, --cap-drop=ALL, --security-opt no-new-privileges,
// --pids-limit, --read-only with a default 64 MiB tmpfs at /tmp.
// Manifest-declared tmpfs entries (validated at check time) extend
// the read-only rootfs with additional writable scratch in a
// deterministic, sorted order. No --publish: Caddy reaches the
// service over the shared `ingress` network by container DNS
// (ADR-0006 Cut 2). Manifest-declared memory, CPU, and
// NET_BIND_SERVICE settings render to the closed set of §7 flags.
func buildPodmanRunArgs(app, env, svcName string, svc config.Service, imageTag, userID, groupID string, envFileExists bool) []string {
	containerName := identity.ContainerName(app, env, svcName)
	shared := identity.SharedDir(app, env)
	appNet := identity.Network(app, env)
	envFile := identity.EnvFile(app, env)

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--restart", "unless-stopped",
		"--user", userID + ":" + groupID,
		"--cap-drop", "ALL",
	}
	if svc.NetBindService != nil && *svc.NetBindService {
		args = append(args, "--cap-add", "NET_BIND_SERVICE")
	}
	args = append(args,
		"--security-opt", "no-new-privileges",
		"--pids-limit", "512",
		"--read-only",
		// mode=1777 (sticky world-writable) so the per-env container
		// user (--user above) can actually write here. Without it,
		// the tmpfs is owned by root and the unprivileged container
		// process fails with EACCES. Same shape applies to every
		// manifest-declared tmpfs below.
		"--tmpfs", "/tmp:size=64m,mode=1777",
		"--network", appNet,
		"--network", "ingress",
		"-v", shared+":"+shared+":Z",
		"--label", "app="+app,
		"--label", "env="+env,
		"--label", "service="+svcName,
	)
	// Mirror the `simple_vps_release` label from the image onto the
	// container so `podman ps --format json` surfaces it without an
	// extra `podman image inspect` hop. The image tag is canonical
	// `simple-vps/<app>-<env>:<sha>`; pull the SHA off the end.
	if i := strings.LastIndex(imageTag, ":"); i > 0 && i < len(imageTag)-1 {
		args = append(args, "--label", "simple_vps_release="+imageTag[i+1:])
	}
	if svc.Memory != nil {
		args = append(args, "--memory", *svc.Memory)
	}
	if svc.CPUs != nil {
		args = append(args, "--cpus", strconv.FormatFloat(*svc.CPUs, 'f', -1, 64))
	}
	for _, path := range sortedKeys(svc.Tmpfs) {
		args = append(args, "--tmpfs", path+":size="+svc.Tmpfs[path]+",mode=1777")
	}
	if envFileExists {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, imageTag)
	if svc.Command != "" {
		// Override the image CMD via /bin/sh -c so users can write the
		// command as a single string (ADR-0005 §13).
		args = append(args, "/bin/sh", "-c", svc.Command)
	}
	return args
}

func startService(app, env, svcName string, svc config.Service, imageTag, userID, groupID string) error {
	containerName := identity.ContainerName(app, env, svcName)
	envFile := identity.EnvFile(app, env)

	// Stop and remove any existing container of the same name.
	_, _ = utils.RunChecked("podman", []string{"rm", "-f", containerName}, "")

	envFileExists := false
	if _, err := os.Stat(envFile); err == nil {
		envFileExists = true
	}
	args := buildPodmanRunArgs(app, env, svcName, svc, imageTag, userID, groupID, envFileExists)

	if _, err := utils.RunChecked("podman", args, ""); err != nil {
		return fmt.Errorf("podman run %s: %v", containerName, err)
	}

	if svc.Port != nil && svc.Healthcheck != "" {
		if err := waitHealthy(containerName, *svc.Port, svc.Healthcheck, healthcheckTimeout(svc)); err != nil {
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

// waitHealthy probes the app container's healthcheck via Caddy on the
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
		lastErr = fmt.Errorf("%s: %s", url, detail)
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

func renderAppCaddyfile(app, env string, ctx *config.AppContext) (string, error) {
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
			// Container DNS over the shared `ingress` network
			// (ADR-0006 Cut 2): Caddy reaches each service by the
			// per-(app, env, service) container name. No host-loopback
			// hop, no port allocator.
			upstream := identity.ContainerName(app, env, route.Service)
			body = fmt.Sprintf("\treverse_proxy http://%s:%d\n", upstream, *svc.Port)
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
		// TLS knob (manifest validation already constrained it to
		// "" / "auto" / "internal"). "internal" emits a self-signed
		// cert directive; "auto" / "" lets Caddy provision Let's
		// Encrypt as normal.
		var prelude string
		if route.TLS == "internal" {
			prelude = "\ttls internal\n"
		}
		// Quote the host on the block selector — hostnames are
		// validated, but `CaddyQuote` is the consistent boundary for
		// every user-controlled string we put into a Caddyfile.
		quotedHost, err := caddy.CaddyQuote(route.Host)
		if err != nil {
			return "", fmt.Errorf("route %q: %v", name, err)
		}
		blocks = append(blocks, fmt.Sprintf("%s {\n%s%s}\n", quotedHost, prelude, body))
	}
	return "# generated by simple-vps server app apply — do not edit\n" + strings.Join(blocks, "\n"), nil
}

func writeAppCaddyfile(app, env string, ctx *config.AppContext) error {
	content, err := renderAppCaddyfile(app, env, ctx)
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
