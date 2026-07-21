// Package podmanruntime translates Ship runtime intent into Podman CLI calls
// and normalizes Podman's tag/ID/JSON quirks for lifecycle callers.
package podmanruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/envelope"
	"github.com/fprl/ship/internal/identity"
	"github.com/fprl/ship/internal/utils"
)

type RunFunc func(args []string) ([]byte, error)

type Runtime struct{ run RunFunc }

func CLI() Runtime {
	return Runtime{run: func(args []string) ([]byte, error) { return utils.RunChecked("podman", args, "") }}
}

func New(run RunFunc) Runtime { return Runtime{run: run} }

type BuildSpec struct {
	App, Env, ImageTag, Release, Dockerfile, ContextDir, EnvelopeLabel string
	Rebuild                                                            bool
}

func BuildArgs(spec BuildSpec) []string {
	args := []string{"build"}
	if spec.Rebuild {
		args = append(args, "--no-cache", "--pull=always")
	}
	args = append(args, "-t", spec.ImageTag,
		"--label", "ship.app="+spec.App,
		"--label", "ship.env="+spec.Env,
		"--label", "ship.release="+spec.Release)
	if spec.EnvelopeLabel != "" {
		args = append(args, "--label", "ship.release_envelope="+spec.EnvelopeLabel)
	}
	return append(args, "-f", spec.Dockerfile, spec.ContextDir)
}

type ContainerSpec struct {
	App, Env, Process, UserID, GroupID, Release, Activation string
	Networks                                                []string
}

func BaseRunArgs(spec ContainerSpec) []string {
	args := []string{"--user", spec.UserID + ":" + spec.GroupID, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "512"}
	for _, network := range spec.Networks {
		args = append(args, "--network", network)
	}
	args = append(args, "-v", identity.DataDir(spec.App, spec.Env)+":/data:Z",
		"--label", "ship.app="+spec.App,
		"--label", "ship.env="+spec.Env,
		"--label", "ship.process="+spec.Process,
		"--label", "ship.release="+spec.Release)
	if spec.Activation != "" {
		args = append(args, "--label", "ship.activation="+spec.Activation)
	}
	return args
}

func WithReadOnlyRoot(args []string) []string {
	return append(args, "--read-only", "--tmpfs", "/tmp:size=64m,mode=1777")
}

func WithResources(args []string, resources config.Resources) []string {
	if resources.Memory != nil {
		args = append(args, "--memory", *resources.Memory)
	}
	if resources.CPUs != nil {
		args = append(args, "--cpus", strconv.FormatFloat(*resources.CPUs, 'f', -1, 64))
	}
	return args
}

type ProcessSpec struct {
	App, Env, Process, Image, UserID, GroupID, Release, Activation, Container, EnvFile string
	Definition                                                                         config.Process
	Preview                                                                            bool
}

func ProcessArgs(spec ProcessSpec) []string {
	args := []string{"run", "--replace", "-d", "--name", spec.Container, "--restart", "unless-stopped"}
	args = append(args, BaseRunArgs(ContainerSpec{
		App: spec.App, Env: spec.Env, Process: spec.Process, UserID: spec.UserID,
		GroupID: spec.GroupID, Release: spec.Release, Activation: spec.Activation,
		Networks: []string{identity.Network(spec.App, spec.Env), "ingress"},
	})...)
	args = WithReadOnlyRoot(args)
	if spec.Definition.Port != nil {
		args = append(args, "--label", "ship.port="+strconv.Itoa(*spec.Definition.Port))
	}
	args = WithResources(args, EffectiveResources(spec.Definition.Resources, spec.Preview))
	if spec.EnvFile != "" {
		args = append(args, "--env-file", spec.EnvFile)
	}
	args = append(args, spec.Image)
	if spec.Definition.Command != "" {
		args = append(args, "/bin/sh", "-c", spec.Definition.Command)
	}
	return args
}

func (r Runtime) StartProcess(spec ProcessSpec) error {
	_, err := r.run(ProcessArgs(spec))
	if err != nil {
		return fmt.Errorf("podman run %s: %w", spec.Container, err)
	}
	return nil
}

func EffectiveResources(resources config.Resources, preview bool) config.Resources {
	if !preview {
		return resources
	}
	if resources.Memory == nil {
		value := "512m"
		resources.Memory = &value
	}
	if resources.CPUs == nil {
		value := 0.5
		resources.CPUs = &value
	}
	return resources
}

type Container struct {
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

func (r Runtime) Containers(app, env string) ([]Container, error) {
	return r.containers([]string{"ps", "-a", "--filter", "label=ship.app=" + app, "--filter", "label=ship.env=" + env, "--format", "json"})
}

func (r Runtime) AllContainers() ([]Container, error) {
	return r.containers([]string{"ps", "-a", "--format", "json"})
}

func (r Runtime) containers(args []string) ([]Container, error) {
	out, err := r.run(args)
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	var entries []Container
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &entries); err != nil {
		return nil, fmt.Errorf("parse podman ps json: %w", err)
	}
	return entries, nil
}

func (r Runtime) ContainerImageIDs(entries []Container) (map[string]string, error) {
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
	out, err := r.run(append([]string{"inspect", "--format", "json"}, names...))
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
		image := item.Image
		if image == "" {
			image = item.Config.Image
		}
		ids[strings.TrimPrefix(item.Name, "/")] = NormalizeImageID(image)
	}
	return ids, nil
}

type ImageEntry struct {
	ID         string            `json:"Id"`
	Repository string            `json:"Repository"`
	Tag        string            `json:"Tag"`
	Names      []string          `json:"Names"`
	Labels     map[string]string `json:"Labels"`
	RepoTags   []string          `json:"RepoTags"`
	CreatedAt  string            `json:"CreatedAt"`
	Config     struct {
		Labels  map[string]string `json:"Labels"`
		Created string            `json:"Created"`
	} `json:"Config"`
}

type Image struct {
	Release      string
	Image        string
	ImageID      string
	ShipTags     []string
	Envelope     envelope.Envelope
	EnvelopeHash string
	CreatedAt    time.Time
}

type MissingImageError struct{ ImageID string }

func (e *MissingImageError) Error() string { return "podman image is absent: " + e.ImageID }

func (r Runtime) InspectImage(imageID string) (ImageEntry, error) {
	out, err := r.run([]string{"image", "inspect", "--format", "json", imageID})
	if err != nil {
		if _, existsErr := r.run([]string{"image", "exists", imageID}); existsErr != nil {
			var commandErr *utils.CommandError
			var exitErr *exec.ExitError
			if errors.As(existsErr, &commandErr) && errors.As(commandErr, &exitErr) && exitErr.ExitCode() == 1 {
				return ImageEntry{}, &MissingImageError{ImageID: imageID}
			}
		}
		return ImageEntry{}, fmt.Errorf("podman image inspect: %w", err)
	}
	data := []byte(strings.TrimSpace(string(out)))
	if len(data) == 0 {
		return ImageEntry{}, errors.New("podman image inspect returned no image")
	}
	var entries []ImageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		var entry ImageEntry
		if singleErr := json.Unmarshal(data, &entry); singleErr != nil {
			return ImageEntry{}, fmt.Errorf("parse podman image inspect json: %w", err)
		}
		entries = []ImageEntry{entry}
	}
	if len(entries) != 1 {
		return ImageEntry{}, fmt.Errorf("podman image inspect returned %d images", len(entries))
	}
	return entries[0], nil
}

func (r Runtime) Images(app, env string) ([]Image, error) {
	out, err := r.run([]string{"images", "--format", "json"})
	if err != nil {
		return nil, err
	}
	data := []byte(strings.TrimSpace(string(out)))
	if len(data) == 0 {
		return nil, fmt.Errorf("podman images returned empty output")
	}
	var entries []ImageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse podman images json: %w", err)
	}
	return ImagesFromEntries(app, env, entries), nil
}

func ImagesFromEntries(app, env string, entries []ImageEntry) []Image {
	out := make([]Image, 0, len(entries))
	byID := map[string]int{}
	for _, entry := range entries {
		labels := entry.Labels
		if labels == nil {
			labels = entry.Config.Labels
		}
		if labels["ship.app"] != app || labels["ship.env"] != env || entry.ID == "" {
			continue
		}
		release := labels["ship.release"]
		if release == "" || release == "<none>" {
			continue
		}
		key := NormalizeImageID(entry.ID)
		if index, ok := byID[key]; ok {
			out[index].ShipTags = appendUniqueShipTags(out[index].ShipTags, app, env, entry)
			continue
		}
		decoded, decodeErr := envelope.DecodeLabel(labels[envelope.Label])
		envelopeHash := ""
		if decodeErr == nil {
			if label, labelErr := decoded.LabelValue(); labelErr == nil {
				envelopeHash = envelope.HashLabel(label)
			}
		}
		created := entry.CreatedAt
		if created == "" {
			created = entry.Config.Created
		}
		out = append(out, Image{Release: release, Image: entry.ID, ImageID: key, ShipTags: appendUniqueShipTags(nil, app, env, entry), Envelope: decoded, EnvelopeHash: envelopeHash, CreatedAt: parseCreatedAt(created)})
		byID[key] = len(out) - 1
	}
	return out
}

func appendUniqueShipTags(existing []string, app, env string, entry ImageEntry) []string {
	repoPrefix := identity.ImageRepo(app, env) + ":"
	seen := map[string]bool{}
	for _, tag := range existing {
		seen[tag] = true
	}
	for _, name := range append(append([]string{}, entry.Names...), entry.RepoTags...) {
		if !seen[name] && strings.HasPrefix(strings.TrimPrefix(name, "localhost/"), repoPrefix) {
			seen[name] = true
			existing = append(existing, name)
		}
	}
	return existing
}

func NormalizeImageID(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
}

func parseCreatedAt(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05 -0700"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func (r Runtime) Stop(name string) error  { _, err := r.run([]string{"stop", name}); return err }
func (r Runtime) Start(name string) error { _, err := r.run([]string{"start", name}); return err }
func (r Runtime) RemoveContainer(name string) error {
	_, err := r.run([]string{"rm", "-f", name})
	return err
}
func (r Runtime) RemoveImage(image string) error { _, err := r.run([]string{"rmi", image}); return err }
func (r Runtime) Tag(image, tag string) error {
	_, err := r.run([]string{"tag", image, tag})
	return err
}
func (r Runtime) CreateNetwork(name string) error {
	_, err := r.run([]string{"network", "create", name})
	return err
}
func (r Runtime) RemoveNetwork(name string) error {
	_, err := r.run([]string{"network", "rm", name})
	return err
}
