package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/secrets"
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
	authorizeOrDie(helperVerbRead, authTargetForAppEnv(c.App, c.Env, "status"))
	withAppEnvLock(c.App, c.Env, func() { c.runLocked() })
	return nil
}

func (c appStatusCmd) runLocked() {
	out, err := podmanPSContainers(c.App, c.Env)
	if err != nil {
		utils.DieError(err, 1)
	}
	processes := containersToProcesses(out)
	if processes == nil {
		processes = []processStatus{}
	}
	pointer, pointerErr := readActive(c.App, c.Env)
	if pointerErr == nil && !pointer.IsLegacy() && pointer.Artifact.ImageID != "" {
		if imageIDs, inspectErr := podmanContainerImageIDs(out); inspectErr == nil {
			for i := range processes {
				processes[i].Image = imageIDs[processes[i].Container]
			}
		}
	}
	attachProcessReleaseMetadata(c.App, c.Env, processes, pointer)
	envKnown := envIdentityExists(c.App, c.Env)
	var static *staticStatus
	release := activeStatusRelease(runningProcesses(processes), static)
	if pointerErr == nil {
		if pointer.IsLegacy() {
			release = &statusRelease{Release: pointer.Legacy.Release, State: "degraded", Detail: "legacy_activation", Next: "ship"}
		} else if resolved, resolveErr := resolveArtifact(c.App, c.Env, pointer.Artifact); resolveErr != nil {
			release = &statusRelease{Release: pointer.Artifact.DisplayIdentity(), Artifact: pointer.Artifact, State: "degraded", Detail: "artifact_unavailable", Next: "ship"}
		} else {
			static = staticStatusFromResolved(c.App, c.Env, resolved)
			intended := statusReleaseFromResolved(resolved)
			if !activePointerRuntimeConvergedResolved(c.App, c.Env, pointer, resolved, processes, static) {
				intended.State = committedNotConvergedState
				intended.Next = convergenceNextStep
			}
			release = &intended
		}
	}
	if c.JSON {
		if static != nil && static.Routes == nil {
			static.Routes = []string{}
		}
		payload := statusPayload{App: c.App, Env: c.Env, Release: release, Static: static, Processes: processes}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return
	}
	fmt.Print(renderStatusText(c.App, c.Env, processes, envKnown, release, static))
}

func staticStatusFromResolved(app, env string, resolved resolvedArtifact) *staticStatus {
	if !resolved.Context.HasStaticRoutes || resolved.Tuple.StaticHash == "" {
		return nil
	}
	if _, err := os.Stat(staticReleasePath(app, env, resolved.Tuple.Release, resolved.Tuple.StaticHash)); err != nil {
		return nil
	}
	routes := make([]string, 0)
	for name, route := range resolved.Context.Routes {
		if route.Serve != "" {
			routes = append(routes, name)
		}
	}
	sort.Strings(routes)
	status := &staticStatus{Release: resolved.Tuple.DisplayIdentity(), RawRelease: resolved.Tuple.Release, Artifact: resolved.Tuple, Routes: routes}
	if meta, err := releaseMetadataFromEnvelope(resolved.Envelope, resolved.Tuple.Release); err == nil {
		status.Dirty, status.BaseCommit, status.CreatedAt = meta.Dirty, meta.BaseCommit, meta.CreatedAt
	}
	return status
}

func statusReleaseFromResolved(resolved resolvedArtifact) statusRelease {
	release := statusRelease{Release: resolved.Tuple.DisplayIdentity(), Artifact: resolved.Tuple}
	if meta, err := releaseMetadataFromEnvelope(resolved.Envelope, resolved.Tuple.Release); err == nil {
		release.Dirty, release.BaseCommit, release.CreatedAt = meta.Dirty, meta.BaseCommit, meta.CreatedAt
	}
	return release
}

// appLsCmd merges durable env identity anchors with live process labels.
// Static-only apps have no containers, so the identity file is the source
// for "this env exists"; process rows still come from Podman labels.
type appLsCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of the text table."`
}

func (c appLsCmd) Run() error {
	authorizeOrDie(helperVerbRead, authTargetForBox("status"))
	apps, err := appListStatuses()
	if err != nil {
		utils.DieError(err, 1)
	}
	appList := appListFromStatuses(apps, time.Now().UTC())
	if c.JSON {
		buf, err := json.MarshalIndent(appList, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Print(renderAppListText(appList))
	return nil
}

func appListStatuses() ([]appEnvStatus, error) {
	out, err := podmanPSAllContainers()
	if err != nil {
		return nil, err
	}
	identityApps, err := identityAppEnvs()
	if err != nil {
		return nil, err
	}
	apps := mergeAppEnvs(identityApps, containersToAppEnvs(out))
	if err := attachAppListRuntimeMetadata(apps); err != nil {
		return nil, err
	}
	return apps, nil
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
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Defaults to 100 when omitted; use 0 with --follow to stream new lines only."`
}

func (c appLogsCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	args := []string{"logs"}
	if c.Process != "" {
		args = append(args, "process="+c.Process)
	}
	authorizeOrDie(helperVerbRead, authTargetForAppEnv(c.App, c.Env, "logs", args...))
	containerName, err := resolveLogContainer(c.App, c.Env, c.Process)
	if err != nil {
		utils.DieError(err, 1)
	}
	logArgs := appLogsPodmanArgs(c.Follow, c.Tail, containerName)
	cmd := exec.Command("podman", logArgs...)
	if c.Follow {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// `podman logs -f` on a stopped container exits cleanly when
			// the container goes away; only surface real errors.
			utils.Die(fmt.Sprintf("podman logs %s: %v", containerName, err), 1)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		utils.Die(fmt.Sprintf("podman logs %s: %s", containerName, detail), 1)
	}
	writeBufferedLogs(&stdout, &stderr, os.Stdout, os.Stderr)
	return nil
}

func writeBufferedLogs(stdout, stderr *bytes.Buffer, stdoutWriter, stderrWriter io.Writer) {
	if stderr.Len() > 0 {
		_, _ = stderrWriter.Write(stderr.Bytes())
	}
	if stdout.Len() == 0 && stderr.Len() == 0 {
		_, _ = fmt.Fprintln(stderrWriter, "no log lines yet")
		return
	}
	_, _ = stdoutWriter.Write(stdout.Bytes())
}

func appLogsPodmanArgs(follow bool, tail int, containerName string) []string {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, "--tail", fmt.Sprintf("%d", tail), containerName)
	return args
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
	Apps []appListAppStatus `json:"apps"`
}

type appListAppStatus struct {
	App  string             `json:"app"`
	Envs []appListEnvStatus `json:"envs"`
}

type appListEnvStatus struct {
	Class          string          `json:"class"`
	Branch         string          `json:"branch"`
	URL            string          `json:"url"`
	CapabilityURL  string          `json:"capability_url,omitempty"`
	Env            string          `json:"env"`
	CurrentRelease string          `json:"current_release"`
	Health         string          `json:"health"`
	AgeSeconds     int64           `json:"age_seconds"`
	ExpiresAt      string          `json:"expires_at"`
	Pinned         bool            `json:"pinned"`
	Dirty          bool            `json:"dirty"`
	BaseCommit     string          `json:"base_commit,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	ShippedBy      *deployIdentity `json:"shipped_by,omitempty"`
	Processes      []processStatus `json:"processes"`
	Static         *staticStatus   `json:"static,omitempty"`
	State          string          `json:"state,omitempty"`
	Detail         string          `json:"detail,omitempty"`
	Next           string          `json:"next,omitempty"`
}

type appEnvStatus struct {
	App        string                    `json:"app"`
	Env        string                    `json:"env"`
	Preview    *identity.PreviewIdentity `json:"preview,omitempty"`
	ShippedBy  *deployIdentity           `json:"shipped_by,omitempty"`
	Processes  []processStatus           `json:"processes"`
	Static     *staticStatus             `json:"static,omitempty"`
	State      string                    `json:"state,omitempty"`
	Detail     string                    `json:"detail,omitempty"`
	Next       string                    `json:"next,omitempty"`
	pointer    activation.Pointer        `json:"-"`
	pointerErr error                     `json:"-"`
	resolved   *resolvedArtifact         `json:"-"`
}

type processStatus struct {
	Process    string `json:"process"`
	Container  string `json:"container"`
	State      string `json:"state"`
	Image      string `json:"image,omitempty"`
	Release    string `json:"release,omitempty"`
	Activation string `json:"activation,omitempty"`
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
	RawRelease string   `json:"-"`
	Artifact   Tuple    `json:"-"`
}

type statusRelease struct {
	Release        string `json:"release,omitempty"`
	Dirty          bool   `json:"dirty,omitempty"`
	BaseCommit     string `json:"base_commit,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	Mixed          bool   `json:"mixed,omitempty"`
	ProcessRelease string `json:"process_release,omitempty"`
	StaticRelease  string `json:"static_release,omitempty"`
	State          string `json:"state,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Next           string `json:"next,omitempty"`
	Artifact       Tuple  `json:"-"`
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
			Process:    proc,
			Container:  name,
			State:      e.State,
			Image:      e.Image,
			Release:    release,
			Activation: e.Labels["ship.activation"],
			Status:     e.Status,
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

func attachProcessReleaseMetadata(app, env string, processes []processStatus, pointer activation.Pointer) {
	if pointer.IsLegacy() || pointer.Artifact.ImageID == "" {
		return
	}
	resolved, err := resolveArtifact(app, env, pointer.Artifact)
	if err != nil {
		return
	}
	attachProcessReleaseMetadataResolved(processes, pointer, resolved)
}

func attachProcessReleaseMetadataResolved(processes []processStatus, pointer activation.Pointer, resolved resolvedArtifact) {
	meta, err := releaseMetadataFromEnvelope(resolved.Envelope, pointer.Artifact.Release)
	if err != nil {
		return
	}
	for i := range processes {
		if processes[i].Release != pointer.Artifact.Release {
			continue
		}
		processes[i].Dirty = meta.Dirty
		processes[i].BaseCommit = meta.BaseCommit
		processes[i].CreatedAt = meta.CreatedAt
	}
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
		if release.State != "" {
			fmt.Fprintf(&b, "  state: %s\n", release.State)
			fmt.Fprintf(&b, "  next: %s\n", release.Next)
		}
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

func renderAppListText(payload appListPayload) string {
	if len(payload.Apps) == 0 {
		return "no apps found\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-16s %-10s %-24s %-36s %-18s %-9s %-8s %-20s %s\n", "APP", "CLASS", "BRANCH", "URL", "RELEASE", "HEALTH", "AGE", "EXPIRES", "SHIPPED BY")
	for _, app := range payload.Apps {
		for _, env := range app.Envs {
			class := "Production"
			if env.Class == "preview" {
				class = "Preview"
			}
			release := env.CurrentRelease
			if release == "" {
				release = "-"
			}
			if env.Dirty {
				release += " (dirty)"
			}
			expires := "-"
			if env.Class == "preview" {
				if env.Pinned {
					expires = "pinned"
				} else if env.ExpiresAt != "" {
					expires = env.ExpiresAt
				}
			}
			shippedBy := "-"
			if env.ShippedBy != nil {
				shippedBy = fmt.Sprintf("%s (%s)", env.ShippedBy.GitAuthor, env.ShippedBy.SSHKeyComment)
			}
			fmt.Fprintf(&b, "%-16s %-10s %-24s %-36s %-18s %-9s %-8s %-20s %s\n",
				app.App, class, env.Branch, dashIfEmptyText(env.URL), release, env.Health, renderAge(env.AgeSeconds), expires, shippedBy)
		}
	}
	return b.String()
}

func dashIfEmptyText(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func appListFromStatuses(statuses []appEnvStatus, now time.Time) appListPayload {
	grouped := map[string][]appListEnvStatus{}
	for _, item := range statuses {
		env := appListEnvFromStatus(item, now)
		grouped[item.App] = append(grouped[item.App], env)
	}
	apps := make([]appListAppStatus, 0, len(grouped))
	for app, envs := range grouped {
		if envs == nil {
			envs = []appListEnvStatus{}
		}
		sort.Slice(envs, func(i, j int) bool {
			if envs[i].Class != envs[j].Class {
				return envs[i].Class == "production"
			}
			return envs[i].Branch < envs[j].Branch
		})
		apps = append(apps, appListAppStatus{App: app, Envs: envs})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].App < apps[j].App })
	return appListPayload{Apps: apps}
}

func appListEnvFromStatus(item appEnvStatus, now time.Time) appListEnvStatus {
	class := "production"
	branch := "main"
	expiresAt := ""
	pinned := false
	if item.Preview != nil {
		class = "preview"
		branch = item.Preview.Branch
		pinned = names.PreviewPinned(item.Preview.ExpiresAt)
		if item.Preview.ExpiresAt != nil {
			expiresAt = item.Preview.ExpiresAt.Format(time.RFC3339Nano)
		}
	} else if item.Env != productionEnvName {
		class = "preview"
		branch = item.Env
	}
	url := ""
	capabilityURL := ""
	if item.pointerErr == nil && !item.pointer.IsLegacy() && item.resolved != nil {
		ctx := item.resolved.Context
		url = execDeploymentURL(ctx)
		if class == "preview" && url != "" {
			if token, tokenErr := previewCapability(item.App, item.Env); tokenErr == nil {
				parsed, parseErr := neturl.Parse(url)
				if parseErr == nil {
					query := parsed.Query()
					query.Set("ship", token)
					parsed.RawQuery = query.Encode()
					capabilityURL = parsed.String()
				}
			}
		}
		if item.Preview == nil && item.Env == productionEnvName {
			branch = ctx.ProductionBranch
		}
	}
	release := activeStatusRelease(item.Processes, item.Static)
	currentRelease := ""
	dirty := false
	createdAt := ""
	baseCommit := ""
	if item.resolved != nil && item.pointerErr == nil && !item.pointer.IsLegacy() {
		resolvedRelease := statusReleaseFromResolved(*item.resolved)
		currentRelease = resolvedRelease.Release
		dirty = resolvedRelease.Dirty
		baseCommit = resolvedRelease.BaseCommit
		createdAt = resolvedRelease.CreatedAt
	} else if release != nil {
		currentRelease = release.Release
		if release.Mixed {
			currentRelease = "mixed"
		}
		dirty = release.Dirty
		createdAt = release.CreatedAt
	}
	processes := item.Processes
	if processes == nil {
		processes = []processStatus{}
	}
	if item.Static != nil && item.Static.Routes == nil {
		static := *item.Static
		static.Routes = []string{}
		item.Static = &static
	}
	return appListEnvStatus{
		Class:          class,
		Branch:         branch,
		URL:            url,
		CapabilityURL:  capabilityURL,
		Env:            item.Env,
		CurrentRelease: currentRelease,
		Health:         appListHealth(item),
		AgeSeconds:     appListAgeSeconds(createdAt, now),
		ExpiresAt:      expiresAt,
		Pinned:         pinned,
		Dirty:          dirty,
		BaseCommit:     baseCommit,
		CreatedAt:      createdAt,
		ShippedBy:      item.ShippedBy,
		Processes:      processes,
		Static:         item.Static,
		State:          item.State,
		Detail:         item.Detail,
		Next:           item.Next,
	}
}

func appListHealth(item appEnvStatus) string {
	if item.State != "" {
		return "degraded"
	}
	if len(item.Processes) == 0 {
		if item.Static != nil {
			return "running"
		}
		return "stopped"
	}
	for _, proc := range item.Processes {
		if proc.State != "running" {
			return "degraded"
		}
	}
	return "running"
}

func appListAgeSeconds(createdAt string, now time.Time) int64 {
	if createdAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return 0
	}
	age := now.Sub(t)
	if age < 0 {
		return 0
	}
	return int64(age.Seconds())
}

func renderAge(seconds int64) string {
	switch {
	case seconds <= 0:
		return "-"
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}

func attachAppListRuntimeMetadata(apps []appEnvStatus) error {
	for i := range apps {
		pointer, pointerErr := readActive(apps[i].App, apps[i].Env)
		apps[i].pointer = pointer
		apps[i].pointerErr = pointerErr
		if pointerErr == nil && pointer.IsLegacy() {
			apps[i].State = "degraded"
			apps[i].Detail = "legacy_activation"
			apps[i].Next = "ship"
			continue
		}
		if errcat.Is(pointerErr, errcat.CodeNoDeploys) {
			continue
		}
		var resolved resolvedArtifact
		if pointerErr == nil && !pointer.IsLegacy() {
			resolved, pointerErr = resolveArtifact(apps[i].App, apps[i].Env, pointer.Artifact)
			if pointerErr == nil {
				apps[i].resolved = &resolved
				attachProcessReleaseMetadataResolved(apps[i].Processes, pointer, resolved)
			}
		}
		if pointerErr != nil {
			apps[i].State = "degraded"
			apps[i].Detail = "artifact_unavailable"
			apps[i].Next = "ship"
			continue
		}
		static := staticStatusFromResolved(apps[i].App, apps[i].Env, resolved)
		apps[i].Static = static
		if !pointer.IsLegacy() && !activePointerRuntimeConvergedResolved(apps[i].App, apps[i].Env, pointer, resolved, apps[i].Processes, static) {
			apps[i].State = committedNotConvergedState
			apps[i].Next = convergenceNextStep
		}
		if entry, torn, err := readLatestSuccessfulDeployJournalEntryWithStatus(apps[i].App, apps[i].Env); torn {
			warnTornDeployJournal(identity.DeployJournalFile(apps[i].App, apps[i].Env))
			if err == nil {
				actor := entry.Identity
				apps[i].ShippedBy = &actor
			}
		} else if err == nil {
			actor := entry.Identity
			apps[i].ShippedBy = &actor
		}
	}
	return nil
}

const committedNotConvergedState = "committed, not converged"

func activePointerRuntimeConvergedResolved(app, env string, pointer activation.Pointer, resolved resolvedArtifact, processes []processStatus, static *staticStatus) bool {
	ctxValue := *resolved.Context
	ctxValue.Routes = make(map[string]config.Route, len(resolved.Context.Routes))
	for name, route := range resolved.Context.Routes {
		ctxValue.Routes[name] = route
	}
	ctx := &ctxValue
	if env != productionEnvName {
		token, err := secrets.GetPreviewCapability(app, env)
		if err != nil {
			return false
		}
		ctx.PreviewCapabilityToken = string(token)
		// The exact predicate reproduces the converge-time render, including
		// the ownership-checked alias attach — a fragment carrying an alias
		// this env no longer owns must read as not converged.
		if _, _, err := attachOwnedPreviewAlias(app, env, ctx); err != nil {
			return false
		}
	}
	desiredNames := map[string]string{}
	// podman ps reports a tag name in Image, not the ID; runtime identity is
	// the exact image ID, so resolve names through container inspect exactly
	// like converge does. Unprovable = not converged.
	imageIDs := map[string]string{}
	if ctx.NeedsImage {
		var entries []containerEntry
		for _, process := range processes {
			if process.State == "running" && process.Container != "" {
				entries = append(entries, containerEntry{Names: []string{process.Container}})
			}
		}
		var inspectErr error
		imageIDs, inspectErr = podmanContainerImageIDs(entries)
		if inspectErr != nil {
			return false
		}
	}
	runtimeImageID := func(process processStatus) string {
		if id := imageIDs[process.Container]; id != "" {
			return id
		}
		return process.Image
	}
	if ctx.NeedsImage {
		for name := range ctx.Processes {
			count := 0
			for _, process := range processes {
				if process.Process == name && process.State == "running" && process.Release == pointer.Artifact.Release && process.Activation == pointer.Activation && (pointer.Artifact.ImageID == "" || normalizeImageID(runtimeImageID(process)) == normalizeImageID(pointer.Artifact.ImageID)) {
					count++
					if process.Container != "" {
						desiredNames[name] = process.Container
					}
				}
			}
			if count != 1 {
				return false
			}
		}
	}
	for _, process := range processes {
		if process.State == "running" {
			if !ctx.NeedsImage || process.Release != pointer.Artifact.Release || process.Activation != pointer.Activation || (pointer.Artifact.ImageID != "" && normalizeImageID(runtimeImageID(process)) != normalizeImageID(pointer.Artifact.ImageID)) {
				return false
			}
			if _, ok := ctx.Processes[process.Process]; !ok {
				return false
			}
		}
	}
	if ctx.HasStaticRoutes && (static == nil || static.Artifact != pointer.Artifact) {
		return false
	}
	fragment, err := os.ReadFile(caddyfilePath(app, env))
	if err != nil {
		return false
	}
	expected, err := renderAppCaddyfileWithProcessNames(app, env, ctx, pointer.Artifact.Release, desiredNames)
	if err != nil || string(fragment) != expected {
		return false
	}
	return true
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
	staticDisplayRelease := ""
	if static != nil {
		staticRelease = static.RawRelease
		if staticRelease == "" {
			staticRelease = static.Release
		}
		staticDisplayRelease = static.Release
	}
	switch {
	case processMixed:
		release := statusRelease{Mixed: true, ProcessRelease: "mixed", StaticRelease: staticRelease}
		return &release
	case processRelease != "" && staticRelease != "" && processRelease != staticRelease:
		return &statusRelease{
			Mixed:          true,
			ProcessRelease: processRelease,
			StaticRelease:  firstNonEmpty(staticDisplayRelease, staticRelease),
		}
	case processRelease != "":
		release := statusRelease{Release: processRelease}
		copyProcessReleaseMetadata(processes, processRelease, &release)
		if staticRelease == processRelease {
			release.StaticRelease = firstNonEmpty(staticDisplayRelease, staticRelease)
			release.ProcessRelease = processRelease
		}
		return &release
	case staticRelease != "":
		release := statusRelease{Release: firstNonEmpty(staticDisplayRelease, staticRelease)}
		copyStaticReleaseMetadata(static, &release)
		return &release
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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

// --- podman calls ---

func podmanPSContainers(app, env string) ([]containerEntry, error) {
	// `--format json` returns a JSON array of containers matching
	// the label filters server-side. Empty array if nothing matches.
	cmd := exec.Command("podman", "ps", "-a",
		"--filter", "label=ship.app="+app,
		"--filter", "label=ship.env="+env,
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
	var entries []containerEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse podman ps json: %v", err)
	}
	return entries, nil
}

func podmanContainerImageIDs(entries []containerEntry) (map[string]string, error) {
	ids := map[string]string{}
	var names []string
	for _, entry := range entries {
		if len(entry.Names) > 0 {
			names = append(names, entry.Names[0])
		}
	}
	if len(names) == 0 {
		return ids, nil
	}
	args := append([]string{"inspect", "--format", "json"}, names...)
	out, err := utils.RunChecked("podman", args, "")
	if err != nil {
		return nil, fmt.Errorf("podman inspect containers: %w", err)
	}
	var inspected []struct {
		Name   string `json:"Name"`
		Image  string `json:"Image"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &inspected); err != nil {
		return nil, fmt.Errorf("parse podman inspect containers: %w", err)
	}
	for _, item := range inspected {
		name := strings.TrimPrefix(item.Name, "/")
		image := item.Image
		if image == "" {
			image = item.Config.Image
		}
		ids[name] = image
	}
	return ids, nil
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
		out = append(out, appEnvStatus{App: file.App, Env: file.Env, Preview: file.Preview, Processes: []processStatus{}})
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
