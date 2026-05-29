package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/utils"
)

// appStatusCmd inspects what `podman ps` currently sees for one
// (app, env) pair and renders either a text table or a structured
// JSON payload. Read-only — never starts, stops, or removes
// anything.
type appStatusCmd struct {
	App  string `arg:"" help:"App name."`
	Env  string `arg:"" help:"Env name."`
	JSON bool   `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c appStatusCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	out, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	processes := containersToProcesses(out)
	envKnown := envIdentityExists(c.App, c.Env)
	if c.JSON {
		payload := statusPayload{App: c.App, Env: c.Env, Processes: processes}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Print(renderStatusText(c.App, c.Env, processes, envKnown))
	return nil
}

// appListCmd merges durable env identity anchors with live process labels.
// Static-only apps have no containers, so the identity file is the source
// for "this env exists"; process rows still come from Podman labels.
type appListCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c appListCmd) Run() error {
	out, err := podmanPSAllContainers()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	identityApps, err := identityAppEnvs()
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	apps := mergeAppEnvs(identityApps, containersToAppEnvs(out))
	if c.JSON {
		payload := appListPayload{Apps: apps}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Print(renderAppListText(apps))
	return nil
}

// appLogsCmd shells `podman logs` for the requested process's
// container. Process argument is optional only when the (app, env)
// has exactly one container — otherwise it's ambiguous and we
// refuse to guess.
type appLogsCmd struct {
	App     string `arg:"" help:"App name."`
	Env     string `arg:"" help:"Env name."`
	Process string `arg:"" optional:"" help:"Process name. Optional when only one process exists."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines (podman logs -f)."`
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Ignored in --follow mode."`
}

func (c appLogsCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	containerName, err := resolveLogContainer(c.App, c.Env, c.Process)
	if err != nil {
		utils.Die(err.Error(), 1)
	}
	args := []string{"logs"}
	if c.Follow {
		args = append(args, "-f")
	} else {
		args = append(args, fmt.Sprintf("--tail=%d", c.Tail))
	}
	args = append(args, containerName)
	cmd := exec.Command("podman", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// `podman logs -f` on a stopped container exits cleanly when
		// the container goes away; only surface real errors.
		utils.Die(fmt.Sprintf("podman logs %s: %v", containerName, err), 1)
	}
	return nil
}

// --- formatting / parsing ---

type statusPayload struct {
	App       string          `json:"app"`
	Env       string          `json:"env"`
	Processes []processStatus `json:"processes"`
}

type appListPayload struct {
	Apps []appEnvStatus `json:"apps"`
}

type appEnvStatus struct {
	App       string          `json:"app"`
	Env       string          `json:"env"`
	Processes []processStatus `json:"processes"`
}

type processStatus struct {
	Process   string `json:"process"`
	Container string `json:"container"`
	State     string `json:"state"`
	Image     string `json:"image,omitempty"`
	Release   string `json:"release,omitempty"`
	Status    string `json:"status,omitempty"` // e.g. "Up 4 minutes"
}

// containerEntry is the slice of `podman ps --format json` we care
// about. Podman's full schema has dozens of fields; pinning a narrow
// surface here keeps us from breaking if upstream re-shuffles
// rarely-used fields.
type containerEntry struct {
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

func containersToProcesses(entries []containerEntry) []processStatus {
	out := make([]processStatus, 0, len(entries))
	for _, e := range entries {
		// `simple-vps.process` label is set by `server app apply` on every
		// container it starts. Anything without it isn't ours and
		// shouldn't surface in app status.
		proc := e.Labels["simple-vps.process"]
		if proc == "" || proc == "release" {
			continue
		}
		name := ""
		if len(e.Names) > 0 {
			name = e.Names[0]
		}
		out = append(out, processStatus{
			Process:   proc,
			Container: name,
			State:     e.State,
			Image:     e.Image,
			Release:   e.Labels["simple-vps.release"],
			Status:    e.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Process < out[j].Process })
	return out
}

func runningProcesses(processes []processStatus) []processStatus {
	out := make([]processStatus, 0, len(processes))
	for _, p := range processes {
		if p.State == "running" {
			out = append(out, p)
		}
	}
	return out
}

func containersToAppEnvs(entries []containerEntry) []appEnvStatus {
	type key struct {
		app string
		env string
	}
	grouped := map[key][]containerEntry{}
	for _, e := range entries {
		app := e.Labels["simple-vps.app"]
		env := e.Labels["simple-vps.env"]
		process := e.Labels["simple-vps.process"]
		if app == "" || env == "" || process == "" || process == "release" {
			continue
		}
		k := key{app: app, env: env}
		grouped[k] = append(grouped[k], e)
	}

	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].app != keys[j].app {
			return keys[i].app < keys[j].app
		}
		return keys[i].env < keys[j].env
	})

	out := make([]appEnvStatus, 0, len(keys))
	for _, k := range keys {
		out = append(out, appEnvStatus{
			App:       k.app,
			Env:       k.env,
			Processes: containersToProcesses(grouped[k]),
		})
	}
	return out
}

func mergeAppEnvs(identityApps, processApps []appEnvStatus) []appEnvStatus {
	type key struct {
		app string
		env string
	}
	grouped := map[key]appEnvStatus{}
	for _, app := range identityApps {
		grouped[key{app: app.App, env: app.Env}] = appEnvStatus{App: app.App, Env: app.Env}
	}
	for _, app := range processApps {
		grouped[key{app: app.App, env: app.Env}] = app
	}
	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].app != keys[j].app {
			return keys[i].app < keys[j].app
		}
		return keys[i].env < keys[j].env
	})
	out := make([]appEnvStatus, 0, len(keys))
	for _, k := range keys {
		out = append(out, grouped[k])
	}
	return out
}

func renderStatusText(app, env string, processes []processStatus, envKnown bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", app, env)
	if len(processes) == 0 {
		if envKnown {
			b.WriteString("  no processes running\n")
		} else {
			b.WriteString("  no processes running — run `simple-vps deploy " + env + "`\n")
		}
		return b.String()
	}
	for _, s := range processes {
		release := s.Release
		if release == "" {
			release = "?"
		}
		state := s.State
		if s.Status != "" {
			state = s.State + " (" + s.Status + ")"
		}
		fmt.Fprintf(&b, "  %-12s %s  release=%s\n", s.Process, state, release)
	}
	return b.String()
}

func renderAppListText(apps []appEnvStatus) string {
	if len(apps) == 0 {
		return "no apps found\n"
	}
	var b strings.Builder
	for _, app := range apps {
		fmt.Fprintf(&b, "%s (%s)\n", app.App, app.Env)
		if len(app.Processes) == 0 {
			b.WriteString("  no processes\n")
			continue
		}
		for _, s := range app.Processes {
			release := s.Release
			if release == "" {
				release = "?"
			}
			state := s.State
			if s.Status != "" {
				state = s.State + " (" + s.Status + ")"
			}
			fmt.Fprintf(&b, "  %-12s %s  release=%s\n", s.Process, state, release)
		}
	}
	return b.String()
}

// --- podman calls ---

func podmanPSContainers(app, env string) ([]containerEntry, error) {
	// `--format json` returns a JSON array of containers matching
	// the label filters server-side. Empty array if nothing matches.
	cmd := exec.Command("podman", "ps", "-a",
		"--filter", "label=simple-vps.app="+app,
		"--filter", "label=simple-vps.env="+env,
		"--format", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %v", err)
	}
	return parsePodmanPSJSON(out)
}

func podmanPSAllContainers() ([]containerEntry, error) {
	cmd := exec.Command("podman", "ps", "-a", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %v", err)
	}
	return parsePodmanPSJSON(out)
}

func parsePodmanPSJSON(out []byte) ([]containerEntry, error) {
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var entries []containerEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse podman ps json: %v", err)
	}
	return entries, nil
}

var envIdentityGlob = "/var/apps/*/simple-vps.json"

func identityAppEnvs() ([]appEnvStatus, error) {
	paths, err := filepath.Glob(envIdentityGlob)
	if err != nil {
		return nil, err
	}
	out := make([]appEnvStatus, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %v", path, err)
		}
		var file envIdentityFile
		if err := json.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("parse %s: %v", path, err)
		}
		if file.App == "" || file.Env == "" || file.InfraID != identity.InfraID(file.App, file.Env) {
			return nil, fmt.Errorf("invalid env identity %s", path)
		}
		out = append(out, appEnvStatus{App: file.App, Env: file.Env})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].Env < out[j].Env
	})
	return out, nil
}

func envIdentityExists(app, env string) bool {
	_, err := os.Stat(identity.IdentityFile(app, env))
	return err == nil
}

func resolveLogContainer(app, env, process string) (string, error) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		return "", err
	}
	processes := containersToProcesses(entries)
	if len(processes) == 0 {
		return "", fmt.Errorf("no processes running for %s (%s)", app, env)
	}
	if process != "" {
		for _, s := range processes {
			if s.Process == process {
				return s.Container, nil
			}
		}
		return "", fmt.Errorf("no process %q for %s (%s)", process, app, env)
	}
	if len(processes) > 1 {
		var names []string
		for _, s := range processes {
			names = append(names, s.Process)
		}
		return "", fmt.Errorf("multiple processes running (%s); pass one as the process argument", strings.Join(names, ", "))
	}
	return processes[0].Container, nil
}
