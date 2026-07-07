package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
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
		utils.DieError(err, 1)
	}
	out, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	processes := containersToProcesses(out)
	if err := attachProcessReleaseMetadata(c.App, c.Env, processes); err != nil {
		utils.DieError(err, 1)
	}
	envKnown := envIdentityExists(c.App, c.Env)
	static, err := activeStaticStatus(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	release := activeStatusRelease(runningProcesses(processes), static)
	if c.JSON {
		payload := statusPayload{App: c.App, Env: c.Env, Release: release, Static: static, Processes: processes}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Print(renderStatusText(c.App, c.Env, processes, envKnown, release, static))
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
		utils.DieError(err, 1)
	}
	identityApps, err := identityAppEnvs()
	if err != nil {
		utils.DieError(err, 1)
	}
	apps := mergeAppEnvs(identityApps, containersToAppEnvs(out))
	if err := attachAppListRuntimeMetadata(apps); err != nil {
		utils.DieError(err, 1)
	}
	if c.JSON {
		payload := appListPayload{Apps: apps}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
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
		utils.DieError(err, 1)
	}
	containerName, err := resolveLogContainer(c.App, c.Env, c.Process)
	if err != nil {
		utils.DieError(err, 1)
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
	Release   *statusRelease  `json:"release,omitempty"`
	Static    *staticStatus   `json:"static,omitempty"`
	Processes []processStatus `json:"processes"`
}

type appListPayload struct {
	Apps []appEnvStatus `json:"apps"`
}

type appEnvStatus struct {
	App       string                    `json:"app"`
	Env       string                    `json:"env"`
	Preview   *identity.PreviewIdentity `json:"preview,omitempty"`
	ShippedBy *deployIdentity           `json:"shipped_by,omitempty"`
	Processes []processStatus           `json:"processes"`
	Static    *staticStatus             `json:"static,omitempty"`
}

type processStatus struct {
	Process    string `json:"process"`
	Container  string `json:"container"`
	State      string `json:"state"`
	Image      string `json:"image,omitempty"`
	Release    string `json:"release,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	Status     string `json:"status,omitempty"` // e.g. "Up 4 minutes"
}

type staticStatus struct {
	Release    string   `json:"release"`
	Routes     []string `json:"routes"`
	Dirty      bool     `json:"dirty,omitempty"`
	BaseCommit string   `json:"base_commit,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
}

type statusRelease struct {
	Release        string `json:"release,omitempty"`
	Dirty          bool   `json:"dirty,omitempty"`
	BaseCommit     string `json:"base_commit,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	Source         string `json:"source"`
	Mixed          bool   `json:"mixed,omitempty"`
	ProcessRelease string `json:"process_release,omitempty"`
	StaticRelease  string `json:"static_release,omitempty"`
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
		// `ship.process` label is set by `server app apply` on every
		// container it starts. Anything without it isn't ours and
		// shouldn't surface in app status.
		proc := e.Labels["ship.process"]
		if proc == "" || isEphemeralProcess(proc) {
			continue
		}
		name := ""
		if len(e.Names) > 0 {
			name = e.Names[0]
		}
		release := e.Labels["ship.release"]
		status := processStatus{
			Process:   proc,
			Container: name,
			State:     e.State,
			Image:     e.Image,
			Release:   release,
			Status:    e.Status,
		}
		out = append(out, status)
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
		app := e.Labels["ship.app"]
		env := e.Labels["ship.env"]
		process := e.Labels["ship.process"]
		if app == "" || env == "" || process == "" || isEphemeralProcess(process) {
			continue
		}
		if e.Labels["ship.infra_id"] != identity.InfraID(app, env) {
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

func attachProcessReleaseMetadata(app, env string, processes []processStatus) error {
	for i := range processes {
		release := processes[i].Release
		if release == "" {
			return fmt.Errorf("process %s for %s (%s) has no release label", processes[i].Process, app, env)
		}
		meta, err := readReleaseMetadata(app, env, release)
		if err != nil {
			return fmt.Errorf("process %s for %s (%s) release %s: %v", processes[i].Process, app, env, release, err)
		}
		processes[i].Dirty = meta.Dirty
		processes[i].BaseCommit = meta.BaseCommit
		processes[i].CreatedAt = meta.CreatedAt
	}
	return nil
}

func mergeAppEnvs(identityApps, processApps []appEnvStatus) []appEnvStatus {
	type key struct {
		app string
		env string
	}
	grouped := map[key]appEnvStatus{}
	for _, app := range identityApps {
		grouped[key{app: app.App, env: app.Env}] = app
	}
	for _, app := range processApps {
		k := key{app: app.App, env: app.Env}
		if existing, ok := grouped[k]; ok {
			app.Preview = existing.Preview
			app.Static = existing.Static
		}
		grouped[k] = app
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

func renderStatusText(app, env string, processes []processStatus, envKnown bool, release *statusRelease, static *staticStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", app, env)
	if release != nil {
		fmt.Fprintf(&b, "  release: %s\n", renderStatusReleaseText(release))
	}
	if len(processes) == 0 && static == nil {
		if envKnown {
			b.WriteString("  no processes running\n")
		} else {
			b.WriteString("  no processes running — run `ship`\n")
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
		if s.Dirty {
			release += " (dirty)"
		}
		fmt.Fprintf(&b, "  %-12s %s  release=%s\n", s.Process, state, release)
	}
	if static != nil {
		staticRelease := static.Release
		if static.Dirty {
			staticRelease += " (dirty)"
		}
		routes := "-"
		if len(static.Routes) > 0 {
			routes = strings.Join(static.Routes, ",")
		}
		fmt.Fprintf(&b, "  %-12s active  release=%s routes=%s\n", "static", staticRelease, routes)
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
			if app.Static == nil {
				b.WriteString("  no processes\n")
				continue
			}
		}
		if app.Static != nil {
			staticRelease := app.Static.Release
			if app.Static.Dirty {
				staticRelease += " (dirty)"
			}
			routes := "-"
			if len(app.Static.Routes) > 0 {
				routes = strings.Join(app.Static.Routes, ",")
			}
			fmt.Fprintf(&b, "  %-12s active  release=%s routes=%s\n", "static", staticRelease, routes)
		}
		if app.ShippedBy != nil {
			fmt.Fprintf(&b, "  shipped by %s (ssh key: %s)\n", app.ShippedBy.GitAuthor, app.ShippedBy.SSHKeyComment)
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
			if s.Dirty {
				release += " (dirty)"
			}
			fmt.Fprintf(&b, "  %-12s %s  release=%s\n", s.Process, state, release)
		}
	}
	return b.String()
}

func attachAppListRuntimeMetadata(apps []appEnvStatus) error {
	for i := range apps {
		if err := attachProcessReleaseMetadata(apps[i].App, apps[i].Env, apps[i].Processes); err != nil {
			return err
		}
		static, err := activeStaticStatus(apps[i].App, apps[i].Env)
		if err != nil {
			return err
		}
		apps[i].Static = static
		if entry, err := readLatestSuccessfulDeployJournalEntry(apps[i].App, apps[i].Env); err == nil {
			actor := entry.Identity
			apps[i].ShippedBy = &actor
		}
	}
	return nil
}

func renderStatusReleaseText(release *statusRelease) string {
	if release.Mixed {
		return fmt.Sprintf("mixed (processes=%s static=%s)", release.ProcessRelease, release.StaticRelease)
	}
	out := release.Release
	if out == "" {
		out = "?"
	}
	if release.Dirty {
		base := release.BaseCommit
		if len(base) > 12 {
			base = base[:12]
		}
		if base != "" {
			out += " (dirty, base " + base + ")"
		} else {
			out += " (dirty)"
		}
	}
	return out
}

func activeStatusRelease(processes []processStatus, static *staticStatus) *statusRelease {
	processRelease, processMixed := commonProcessRelease(processes)
	staticRelease := ""
	if static != nil {
		staticRelease = static.Release
	}
	switch {
	case processMixed:
		release := statusRelease{Source: "mixed", Mixed: true, ProcessRelease: "mixed", StaticRelease: staticRelease}
		return &release
	case processRelease != "" && staticRelease != "" && processRelease != staticRelease:
		return &statusRelease{
			Source:         "mixed",
			Mixed:          true,
			ProcessRelease: processRelease,
			StaticRelease:  staticRelease,
		}
	case processRelease != "":
		release := statusRelease{Release: processRelease, Source: "process"}
		copyProcessReleaseMetadata(processes, processRelease, &release)
		if staticRelease == processRelease {
			release.Source = "mixed"
			release.StaticRelease = staticRelease
			release.ProcessRelease = processRelease
		}
		return &release
	case staticRelease != "":
		release := statusRelease{Release: staticRelease, Source: "static"}
		copyStaticReleaseMetadata(static, &release)
		return &release
	default:
		return nil
	}
}

func commonProcessRelease(processes []processStatus) (string, bool) {
	release := ""
	for _, proc := range processes {
		if proc.Release == "" {
			continue
		}
		if release == "" {
			release = proc.Release
			continue
		}
		if proc.Release != release {
			return "", true
		}
	}
	return release, false
}

func copyProcessReleaseMetadata(processes []processStatus, release string, target *statusRelease) {
	for _, proc := range processes {
		if proc.Release != release {
			continue
		}
		target.Dirty = proc.Dirty
		target.BaseCommit = proc.BaseCommit
		target.CreatedAt = proc.CreatedAt
		return
	}
}

func copyStaticReleaseMetadata(static *staticStatus, target *statusRelease) {
	if static == nil {
		return
	}
	target.Dirty = static.Dirty
	target.BaseCommit = static.BaseCommit
	target.CreatedAt = static.CreatedAt
}

func activeStaticStatus(app, env string) (*staticStatus, error) {
	current := filepath.Join(identity.StaticDir(app, env), "current")
	target, err := os.Readlink(current)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	release := filepath.Base(target)
	if err := validateRelease(release); err != nil {
		return nil, err
	}
	routes, err := staticCurrentRoutes(target)
	if err != nil {
		return nil, err
	}
	status := &staticStatus{Release: release, Routes: routes}
	meta, err := readReleaseMetadata(app, env, release)
	if err != nil {
		return nil, err
	}
	status.Dirty = meta.Dirty
	status.BaseCommit = meta.BaseCommit
	status.CreatedAt = meta.CreatedAt
	return status, nil
}

func staticCurrentRoutes(currentTarget string) ([]string, error) {
	entries, err := os.ReadDir(currentTarget)
	if err != nil {
		return nil, err
	}
	var routes []string
	for _, entry := range entries {
		if entry.IsDir() {
			routes = append(routes, entry.Name())
		}
	}
	sort.Strings(routes)
	return routes, nil
}

// --- podman calls ---

func podmanPSContainers(app, env string) ([]containerEntry, error) {
	// `--format json` returns a JSON array of containers matching
	// the label filters server-side. Empty array if nothing matches.
	cmd := exec.Command("podman", "ps", "-a",
		"--filter", "label=ship.app="+app,
		"--filter", "label=ship.env="+env,
		"--filter", "label=ship.infra_id="+identity.InfraID(app, env),
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

var envIdentityGlob string

func identityGlob() string {
	if envIdentityGlob != "" {
		return envIdentityGlob
	}
	return filepath.Join(identity.AppsRoot(), "*", "ship.json")
}

func identityAppEnvs() ([]appEnvStatus, error) {
	paths, err := filepath.Glob(identityGlob())
	if err != nil {
		return nil, err
	}
	out := make([]appEnvStatus, 0, len(paths))
	for _, path := range paths {
		file, err := readEnvIdentityFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %v", path, err)
		}
		out = append(out, appEnvStatus{App: file.App, Env: file.Env, Preview: file.Preview})
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
