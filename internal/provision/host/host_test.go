package host

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEnsureFileWritesOnlyWhenContentOrMetadataDiffers(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner}
	file := File{
		Path:    "/etc/ship/host.json",
		Content: []byte("one\n"),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	}

	changed, err := EnsureFile(apply, file)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected missing file to be changed")
	}
	assertWrites(t, runner, file.Path)

	changed, err = EnsureFile(apply, file)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected identical file to be unchanged")
	}
	assertWrites(t, runner, file.Path)

	file.Mode = 0600
	changed, err = EnsureFile(apply, file)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected mode drift to be changed")
	}
	if len(runner.writes) != 2 {
		t.Fatalf("expected two writes, got %d", len(runner.writes))
	}
}

func TestEnsureFileCheckModeReportsDriftWithoutWriting(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner, CheckMode: true}

	changed, err := EnsureFile(apply, File{
		Path:    "/etc/ship/host.json",
		Content: []byte("{}\n"),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected missing file to be reported as changed")
	}
	if len(runner.writes) != 0 {
		t.Fatalf("check mode wrote files: %+v", runner.writes)
	}
}

func TestEnsureFileRejectsMissingMode(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureFile(apply, File{
		Path:    "/etc/ship/host.json",
		Content: []byte("{}\n"),
		Owner:   "root",
		Group:   "root",
	})
	if err == nil {
		t.Fatal("expected missing file mode to fail")
	}
	if changed {
		t.Fatal("missing file mode must not report changed")
	}
	if len(runner.writes) != 0 {
		t.Fatalf("missing file mode wrote files: %+v", runner.writes)
	}
}

func TestEnsureSudoersFileValidatesBeforeWriting(t *testing.T) {
	runner := newFakeRunner()
	runner.validateErr = errors.New("bad sudoers")
	apply := Apply{Context: context.Background(), Runner: runner}

	sudoers := testDeploySudoers()
	changed, err := EnsureSudoersFile(apply, "ship", sudoers)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if changed {
		t.Fatal("invalid sudoers content must not report changed")
	}
	if len(runner.writes) != 0 {
		t.Fatalf("invalid sudoers content wrote files: %+v", runner.writes)
	}

	runner.validateErr = nil
	changed, err = EnsureSudoersFile(apply, "ship", sudoers)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected sudoers file to be written")
	}
	got := runner.files["/etc/sudoers.d/ship"]
	if string(got.Content) != string(sudoers)+"\n" {
		t.Fatalf("unexpected sudoers content: %q", string(got.Content))
	}
	if got.Owner != "root" || got.Group != "root" || got.Mode != 0440 {
		t.Fatalf("unexpected sudoers metadata: %+v", got)
	}
}

func TestEnsureSudoersFileRejectsUnsafeName(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner}

	_, err := EnsureSudoersFile(apply, "../root", append(testDeploySudoers(), '\n'))
	if err == nil {
		t.Fatal("expected unsafe sudoers name to fail")
	}
	if len(runner.validations) != 0 || len(runner.writes) != 0 {
		t.Fatalf("unsafe sudoers name touched runner: validations=%+v writes=%+v", runner.validations, runner.writes)
	}
}

func testDeploySudoers() []byte {
	return []byte("deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *, /usr/local/bin/ship server approval *, /usr/local/bin/ship server config *, /usr/local/bin/ship server webhook *, /usr/local/bin/ship server version, /usr/local/bin/ship server version *, /usr/local/bin/ship server update *")
}

func TestEnsureDirectoryRejectsExistingNonDirectory(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"stat -c %U\t%G\t%a\t%F /var/apps": {
			ExitCode: 0,
			Stdout:   []byte("root\troot\t777\tsymbolic link\n"),
		},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureDirectory(apply, Directory{
		Path:  "/var/apps",
		Owner: "root",
		Group: "root",
		Mode:  0755,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected non-directory error, got changed=%v err=%v", changed, err)
	}
	if runner.ranCommand("install", "-d -o root -g root -m 755 /var/apps") {
		t.Fatal("install -d should not run for an existing non-directory")
	}
}

func TestEnsureDirectoryNormalizesExistingDirectoryWithoutTouchingChildren(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"stat -c %U\t%G\t%a\t%F /var/apps": {
			ExitCode: 0,
			Stdout:   []byte("root\troot\t700\tdirectory\n"),
		},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureDirectory(apply, Directory{
		Path:  "/var/apps",
		Owner: "root",
		Group: "root",
		Mode:  0755,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected mode drift to be changed")
	}
	if !runner.ranCommand("install", "-d -o root -g root -m 755 /var/apps") {
		t.Fatalf("expected non-recursive install -d, commands: %+v", runner.commands)
	}
}

func TestEnsureSystemdUnitWritesUnitReloadsDaemonThenRunsRequestedAction(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "caddy.service",
		Content: []byte("[Unit]\nDescription=Caddy\n"),
		Action:  Restarted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected new unit and requested reload to report changed")
	}

	wantCommands := []Command{
		{Program: "systemctl", Args: []string{"daemon-reload"}},
		{Program: "systemctl", Args: []string{"restart", "caddy.service"}},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("unexpected commands:\nwant: %+v\n got: %+v", wantCommands, runner.commands)
	}

	runner.commands = nil
	changed, err = EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "caddy.service",
		Content: []byte("[Unit]\nDescription=Caddy\n"),
		Action:  NoSystemdAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected unchanged unit with no action to report unchanged")
	}
	if len(runner.commands) != 0 {
		t.Fatalf("unchanged unit ran commands: %+v", runner.commands)
	}
}

func TestEnsureSystemdUnitCheckModeDoesNotWriteOrRunCommands(t *testing.T) {
	runner := newFakeRunner()
	apply := Apply{Context: context.Background(), Runner: runner, CheckMode: true}

	changed, err := EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "ship.service",
		Content: []byte("[Service]\nExecStart=/usr/local/bin/ship server\n"),
		Action:  Restarted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected check mode to report pending unit/action change")
	}
	if len(runner.writes) != 0 || len(runner.commands) != 0 {
		t.Fatalf("check mode touched host: writes=%+v commands=%+v", runner.writes, runner.commands)
	}
}

func TestEnsureSystemdUnitStartedUsesServiceState(t *testing.T) {
	runner := newFakeRunner()
	content := []byte("[Unit]\nDescription=Caddy\n")
	runner.files["/etc/systemd/system/caddy.service"] = FileState{
		Content: content,
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "caddy.service",
		Content: content,
		Action:  Started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected already-active service to be unchanged")
	}

	wantCommands := []Command{
		{Program: "systemctl", Args: []string{"is-active", "--quiet", "caddy.service"}},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("unexpected commands:\nwant: %+v\n got: %+v", wantCommands, runner.commands)
	}
}

func TestEnsureSystemdUnitRestartsActiveServiceWhenContentChanges(t *testing.T) {
	runner := newFakeRunner()
	runner.files["/etc/systemd/system/caddy.service"] = FileState{
		Content: []byte("[Unit]\nDescription=Old\n"),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	}
	runner.commandResults = map[string]CommandResult{
		"systemctl is-active --quiet caddy.service": {},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "caddy.service",
		Content: []byte("[Unit]\nDescription=New\n"),
		Action:  Started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed unit to report changed")
	}

	wantCommands := []Command{
		{Program: "systemctl", Args: []string{"daemon-reload"}},
		{Program: "systemctl", Args: []string{"is-active", "--quiet", "caddy.service"}},
		{Program: "systemctl", Args: []string{"restart", "caddy.service"}},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("unexpected commands:\nwant: %+v\n got: %+v", wantCommands, runner.commands)
	}
}

func TestEnsureSystemdUnitCheckModeReportsChangedActiveService(t *testing.T) {
	runner := newFakeRunner()
	runner.files["/etc/systemd/system/caddy.service"] = FileState{
		Content: []byte("[Unit]\nDescription=Old\n"),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	}
	runner.commandResults = map[string]CommandResult{
		"systemctl is-active --quiet caddy.service": {},
	}
	apply := Apply{Context: context.Background(), Runner: runner, CheckMode: true}

	changed, err := EnsureSystemdUnit(apply, SystemdUnit{
		Name:    "caddy.service",
		Content: []byte("[Unit]\nDescription=New\n"),
		Action:  Started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected check mode to report changed active service")
	}
	if len(runner.writes) != 0 {
		t.Fatalf("check mode wrote unit: %+v", runner.writes)
	}
}

func TestEnsureUserCorrectsExistingShellHomeAndPrimaryGroup(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"getent group deploy":  {Stdout: []byte("deploy:x:2000:\n")},
		"getent passwd deploy": {Stdout: []byte("deploy:x:1001:1001::/old:/bin/sh\n")},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureUser(apply, User{
		Name:         "deploy",
		PrimaryGroup: "deploy",
		Shell:        "/bin/bash",
		Home:         "/home/deploy",
		CreateHome:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected user drift to be corrected")
	}

	wantCommands := []Command{
		{Program: "getent", Args: []string{"group", "deploy"}},
		{Program: "getent", Args: []string{"passwd", "deploy"}},
		{Program: "usermod", Args: []string{"--gid", "deploy", "--shell", "/bin/bash", "--home", "/home/deploy", "--move-home", "deploy"}},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("unexpected commands:\nwant: %+v\n got: %+v", wantCommands, runner.commands)
	}
}

func TestEnsureUserSkipsAlreadyConvergedUser(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"getent group deploy":  {Stdout: []byte("deploy:x:2000:\n")},
		"getent passwd deploy": {Stdout: []byte("deploy:x:1001:2000::/home/deploy:/bin/bash\n")},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureUser(apply, User{
		Name:         "deploy",
		PrimaryGroup: "deploy",
		Shell:        "/bin/bash",
		CreateHome:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected converged user to be unchanged")
	}
	if len(runner.commands) != 2 {
		t.Fatalf("expected only getent probes, got %+v", runner.commands)
	}
}

func TestEnsureUfwRuleSkipsAlreadyAppliedRule(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"ufw status verbose": {Stdout: []byte("Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n22/tcp ALLOW IN Anywhere\n")},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureUfwRule(apply, UfwRule{Rule: "allow 22/tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected existing ufw rule to be unchanged")
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected only status probe, got %+v", runner.commands)
	}
}

func TestEnsureUfwRuleReportsMissingDeleteAsUnchanged(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"ufw status verbose": {Stdout: []byte("Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n")},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureUfwRule(apply, UfwRule{Rule: "allow 80/tcp", Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected missing ufw rule delete to be unchanged")
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected only status probe, got %+v", runner.commands)
	}
}

func TestEnsureUfwRuleRunsWhenRuleMissing(t *testing.T) {
	runner := newFakeRunner()
	runner.commandResults = map[string]CommandResult{
		"ufw status verbose": {Stdout: []byte("Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n")},
	}
	apply := Apply{Context: context.Background(), Runner: runner}

	changed, err := EnsureUfwRule(apply, UfwRule{Rule: "allow 22/tcp"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected missing ufw rule to change")
	}
	// Real ufw (0.36+) rejects `ufw --force allow ...` with "Invalid
	// syntax" — --force only applies to prompting commands
	// (enable/reset/delete). EnsureUfwRule must not prepend it for
	// allow/deny.
	wantCommands := []Command{
		{Program: "ufw", Args: []string{"status", "verbose"}},
		{Program: "ufw", Args: []string{"allow", "22/tcp"}},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("unexpected commands:\nwant: %+v\n got: %+v", wantCommands, runner.commands)
	}
}

type fakeRunner struct {
	files          map[string]FileState
	writes         []File
	validations    []Validation
	validateErr    error
	commands       []Command
	commandResults map[string]CommandResult
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{files: make(map[string]FileState)}
}

func (r *fakeRunner) ReadFile(_ context.Context, path string) (FileState, error) {
	file, ok := r.files[path]
	if !ok {
		return FileState{}, ErrNotExist
	}
	return file, nil
}

func (r *fakeRunner) WriteFile(_ context.Context, file File) error {
	r.writes = append(r.writes, file)
	r.files[file.Path] = FileState{
		Content: append([]byte(nil), file.Content...),
		Owner:   file.Owner,
		Group:   file.Group,
		Mode:    file.Mode,
	}
	return nil
}

func (r *fakeRunner) Validate(_ context.Context, validation Validation) error {
	r.validations = append(r.validations, validation)
	return r.validateErr
}

func (r *fakeRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	r.commands = append(r.commands, command)
	if result, ok := r.commandResults[commandKey(command)]; ok {
		return result, nil
	}
	if command.Program == "mktemp" && len(command.Args) == 2 && command.Args[0] == "-d" {
		return CommandResult{Stdout: []byte(strings.TrimSuffix(command.Args[1], ".XXXXXX") + ".TEST\n")}, nil
	}
	return CommandResult{}, nil
}

func (r *fakeRunner) ranCommand(program string, args string) bool {
	for _, command := range r.commands {
		if command.Program == program && strings.Join(command.Args, " ") == args {
			return true
		}
	}
	return false
}

func commandKey(command Command) string {
	return command.Program + " " + strings.Join(command.Args, " ")
}

func assertWrites(t *testing.T, runner *fakeRunner, path string) {
	t.Helper()
	if len(runner.writes) != 1 {
		t.Fatalf("expected one write, got %d", len(runner.writes))
	}
	if runner.writes[0].Path != path {
		t.Fatalf("unexpected write path: %s", runner.writes[0].Path)
	}
}

var _ Runner = (*fakeRunner)(nil)
