package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/utils"
)

// appStatusCmd inspects what `podman ps` currently sees for one
// (app, env) pair and renders either a text table or a structured
// JSON payload. Read-only — never starts, stops, or removes
// anything; that surface lands in a separate restart/destroy PR.
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
	services := containersToServices(out)
	if c.JSON {
		payload := statusPayload{App: c.App, Env: c.Env, Services: services}
		buf, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Print(renderStatusText(c.App, c.Env, services))
	return nil
}

// appLogsCmd shells `podman logs` for the requested service's
// container. Service argument is optional only when the (app, env)
// has exactly one container — otherwise it's ambiguous and we
// refuse to guess.
type appLogsCmd struct {
	App     string `arg:"" help:"App name."`
	Env     string `arg:"" help:"Env name."`
	Service string `arg:"" optional:"" help:"Service name. Optional when only one service exists."`
	Follow  bool   `name:"follow" short:"f" help:"Stream new log lines (podman logs -f)."`
	Tail    int    `name:"tail" default:"100" help:"How many trailing lines to show. Ignored in --follow mode."`
}

func (c appLogsCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	containerName, err := resolveLogContainer(c.App, c.Env, c.Service)
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
	App      string          `json:"app"`
	Env      string          `json:"env"`
	Services []serviceStatus `json:"services"`
}

type serviceStatus struct {
	Service   string `json:"service"`
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

func containersToServices(entries []containerEntry) []serviceStatus {
	out := make([]serviceStatus, 0, len(entries))
	for _, e := range entries {
		// `service` label is set by `server app apply` on every
		// container it starts. Anything without it isn't ours and
		// shouldn't surface in app status.
		svc := e.Labels["service"]
		if svc == "" {
			continue
		}
		name := ""
		if len(e.Names) > 0 {
			name = e.Names[0]
		}
		out = append(out, serviceStatus{
			Service:   svc,
			Container: name,
			State:     e.State,
			Image:     e.Image,
			Release:   e.Labels["simple_vps_release"],
			Status:    e.Status,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func renderStatusText(app, env string, services []serviceStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", app, env)
	if len(services) == 0 {
		b.WriteString("  no services running — run `simple-vps deploy " + env + "`\n")
		return b.String()
	}
	for _, s := range services {
		release := s.Release
		if release == "" {
			release = "?"
		}
		state := s.State
		if s.Status != "" {
			state = s.State + " (" + s.Status + ")"
		}
		fmt.Fprintf(&b, "  %-12s %s  release=%s\n", s.Service, state, release)
	}
	return b.String()
}

// --- podman calls ---

func podmanPSContainers(app, env string) ([]containerEntry, error) {
	// `--format json` returns a JSON array of containers matching
	// the label filters server-side. Empty array if nothing matches.
	cmd := exec.Command("podman", "ps", "-a",
		"--filter", "label=app="+app,
		"--filter", "label=env="+env,
		"--format", "json",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %v", err)
	}
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

func resolveLogContainer(app, env, service string) (string, error) {
	entries, err := podmanPSContainers(app, env)
	if err != nil {
		return "", err
	}
	services := containersToServices(entries)
	if len(services) == 0 {
		return "", fmt.Errorf("no services running for %s (%s)", app, env)
	}
	if service != "" {
		for _, s := range services {
			if s.Service == service {
				return s.Container, nil
			}
		}
		return "", fmt.Errorf("no service %q for %s (%s)", service, app, env)
	}
	if len(services) > 1 {
		var names []string
		for _, s := range services {
			names = append(names, s.Service)
		}
		return "", fmt.Errorf("multiple services running (%s); pass one as the service argument", strings.Join(names, ", "))
	}
	return services[0].Container, nil
}
