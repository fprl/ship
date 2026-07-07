package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotExist   = errors.New("remote file does not exist")
	sudoersNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	unitNameRe    = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.(service|timer)$`)
)

type Apply struct {
	context.Context
	Runner    Runner
	CheckMode bool
	State     *RunState
}

type RunState struct {
	AptUpdated bool
}

type Runner interface {
	ReadFile(ctx context.Context, path string) (FileState, error)
	WriteFile(ctx context.Context, file File) error
	Validate(ctx context.Context, validation Validation) error
	Run(ctx context.Context, command Command) (CommandResult, error)
}

type File struct {
	Path    string
	Content []byte
	Owner   string
	Group   string
	Mode    os.FileMode
}

type FileState struct {
	Content []byte
	Owner   string
	Group   string
	Mode    os.FileMode
}

type Validation struct {
	Kind    ValidationKind
	Content []byte
}

type ValidationKind string

const ValidationSudoers ValidationKind = "sudoers"

type Command struct {
	Program string
	Args    []string
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type SystemdAction string

const (
	NoSystemdAction SystemdAction = ""
	Started         SystemdAction = "start"
	Restarted       SystemdAction = "restart"
)

type SystemdUnit struct {
	Name    string
	Content []byte
	Action  SystemdAction
}

func EnsureFile(apply Apply, file File) (bool, error) {
	if apply.Runner == nil {
		return false, errors.New("provision runner is required")
	}
	if !filepath.IsAbs(file.Path) {
		return false, fmt.Errorf("file path must be absolute: %s", file.Path)
	}
	if file.Mode == 0 {
		return false, fmt.Errorf("file mode is required for %s", file.Path)
	}

	file.Mode = file.Mode.Perm()
	current, err := apply.Runner.ReadFile(apply.ContextOrBackground(), file.Path)
	if err != nil && !errors.Is(err, ErrNotExist) {
		return false, err
	}

	changed := errors.Is(err, ErrNotExist) || fileDiffers(current, file)
	if !changed {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	if err := apply.Runner.WriteFile(apply.ContextOrBackground(), file); err != nil {
		return false, err
	}
	return true, nil
}

func EnsureSudoersFile(apply Apply, name string, content []byte) (bool, error) {
	if apply.Runner == nil {
		return false, errors.New("provision runner is required")
	}
	name = strings.TrimSpace(name)
	if !sudoersNameRe.MatchString(name) {
		return false, fmt.Errorf("invalid sudoers file name: %s", name)
	}

	content = withTrailingNewline(content)
	if err := apply.Runner.Validate(apply.ContextOrBackground(), Validation{Kind: ValidationSudoers, Content: content}); err != nil {
		return false, err
	}

	return EnsureFile(apply, File{
		Path:    filepath.Join("/etc/sudoers.d", name),
		Content: content,
		Owner:   "root",
		Group:   "root",
		Mode:    0440,
	})
}

func EnsureSystemdUnit(apply Apply, unit SystemdUnit) (bool, error) {
	if !unitNameRe.MatchString(unit.Name) {
		return false, fmt.Errorf("invalid systemd unit name: %s", unit.Name)
	}
	if !validSystemdAction(unit.Action) {
		return false, fmt.Errorf("unsupported systemd action: %s", unit.Action)
	}

	unitChanged := false
	if unit.Content != nil {
		var err error
		unitChanged, err = EnsureFile(apply, File{
			Path:    filepath.Join("/etc/systemd/system", unit.Name),
			Content: unit.Content,
			Owner:   "root",
			Group:   "root",
			Mode:    0644,
		})
		if err != nil {
			return false, err
		}
	}

	actionRequested := unit.Action != NoSystemdAction
	if apply.CheckMode {
		actionChanged, err := systemdActionWouldChange(apply, unit)
		if err != nil {
			return false, err
		}
		return unitChanged || actionChanged, nil
	}

	if unitChanged {
		if err := runSystemctl(apply, "daemon-reload"); err != nil {
			return false, err
		}
	}

	switch unit.Action {
	case NoSystemdAction:
	case Started:
		changed, err := ensureServiceStarted(apply, unit.Name)
		if err != nil {
			return false, err
		}
		return unitChanged || changed, nil
	case Restarted:
		if err := runSystemctl(apply, string(unit.Action), unit.Name); err != nil {
			return false, err
		}
	}

	return unitChanged || actionRequested, nil
}

func systemdActionWouldChange(apply Apply, unit SystemdUnit) (bool, error) {
	switch unit.Action {
	case NoSystemdAction:
		return false, nil
	case Started:
		active, err := serviceIsActive(apply, unit.Name)
		return !active, err
	case Restarted:
		return true, nil
	default:
		return false, fmt.Errorf("unsupported systemd action: %s", unit.Action)
	}
}

func validSystemdAction(action SystemdAction) bool {
	switch action {
	case NoSystemdAction, Started, Restarted:
		return true
	default:
		return false
	}
}

func ensureServiceStarted(apply Apply, name string) (bool, error) {
	active, err := serviceIsActive(apply, name)
	if err != nil {
		return false, err
	}
	if active {
		return false, nil
	}
	if err := runSystemctl(apply, "start", name); err != nil {
		return false, err
	}
	// `systemctl start` on Type=simple units returns as soon as the
	// fork is in flight. A unit that fails immediately afterwards (bad
	// config, missing dep) would otherwise be invisible to the
	// installer. Caddy did exactly this on a real-box smoke. Poll
	// briefly so a fast-fail surfaces here.
	if err := waitServiceActive(apply, name); err != nil {
		return false, err
	}
	return true, nil
}

func waitServiceActive(apply Apply, name string) error {
	for attempt := 0; attempt < 10; attempt++ {
		active, err := serviceIsActive(apply, name)
		if err != nil {
			return err
		}
		if active {
			return nil
		}
		// Short, fixed-step backoff. Total ceiling ~1s — enough to
		// catch immediate failures without slowing a healthy install.
		if attempt < 9 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return fmt.Errorf("service %s did not become active after start; inspect with `journalctl -u %s`", name, name)
}

func serviceIsActive(apply Apply, name string) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "systemctl", Args: []string{"is-active", "--quiet", name}})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func runSystemctl(apply Apply, args ...string) error {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "systemctl", Args: args})
	if err != nil {
		return err
	}
	return requireZero(result, "systemctl", args)
}

func (apply Apply) ContextOrBackground() context.Context {
	if apply.Context != nil {
		return apply.Context
	}
	return context.Background()
}

func fileDiffers(current FileState, desired File) bool {
	return !bytes.Equal(current.Content, desired.Content) ||
		current.Owner != desired.Owner ||
		current.Group != desired.Group ||
		current.Mode.Perm() != desired.Mode.Perm()
}

func withTrailingNewline(content []byte) []byte {
	if len(content) == 0 || content[len(content)-1] == '\n' {
		return append([]byte(nil), content...)
	}
	out := make([]byte, 0, len(content)+1)
	out = append(out, content...)
	out = append(out, '\n')
	return out
}

func requireZero(result CommandResult, program string, args []string) error {
	if result.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("command failed: %s %v: exit %d: %s", program, args, result.ExitCode, strings.TrimSpace(string(result.Stderr)))
}
