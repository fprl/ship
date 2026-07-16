package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/journal"
	"github.com/fprl/ship/internal/provision"
	"github.com/fprl/ship/internal/provision/local"
	"github.com/fprl/ship/internal/release"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

type updateHelperCmd struct {
	MemberFingerprint string `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Version           string `name:"version" required:"" help:"Released ship version to converge."`
}

var runVerifiedUpdateLocal = func(binary string) error {
	cmd := exec.Command(binary, "server", "update-local", "--binary", binary)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c updateHelperCmd) BeforeApply() error { return requireRoot() }

func (c updateHelperCmd) Run() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	if _, err := authorizeHelper(helperVerbBoxMutation, authTargetForBox("update box")); err != nil {
		utils.DieError(err, 1)
	}
	if err := validateUpdateTarget(version.Version, c.Version); err != nil {
		return err
	}

	lock, err := acquireBoxUpdateLock()
	if err != nil {
		return err
	}
	defer lock.Release()

	name := "ship-linux-" + runtime.GOARCH
	data, err := release.DownloadVerifiedAsset(environmentMap(), c.Version, name)
	if err != nil {
		return fmt.Errorf("download verified release helper %s: %w; restore outbound HTTPS access to release artifacts, then run `ship box update <box>`", c.Version, err)
	}
	return runVerifiedUpdate(c.Version, func() error {
		binary, cleanup, err := writeVerifiedUpdateBinary(data)
		if err != nil {
			return err
		}
		defer cleanup()
		return runVerifiedUpdateLocal(binary)
	})
}

func validateUpdateTarget(installed, target string) error {
	if !release.IsVersion(target) {
		return fmt.Errorf("box update requires a released version, got %q; run `ship box setup <box>` for a development build", target)
	}
	cmp, ok := version.Compare(installed, target)
	if !ok || cmp >= 0 {
		return fmt.Errorf("box update target %s must be strictly newer than installed helper %s", target, installed)
	}
	return nil
}

func runVerifiedUpdate(target string, mutate func() error) error {
	if err := appendUpdateJournal(updateJournalEntry{Event: "started", Version: target}); err != nil {
		return err
	}
	return mutate()
}

// updateLocalCmd is invoked by the verified release binary after the installed
// helper has checked the member role. Rendering from that binary keeps every
// version-owned artifact aligned with the downloaded release.
type updateLocalCmd struct {
	Binary string `name:"binary" required:""`
}

func (c updateLocalCmd) BeforeApply() error { return requireRoot() }

func (c updateLocalCmd) Run() error {
	if !validUpdateBinary(c.Binary) {
		return fmt.Errorf("invalid update helper path: %s", c.Binary)
	}
	summary, err := provision.RunVersionConverge(context.Background(), local.Runner{}, provision.VersionConvergeOptions{
		HelperBinaryPath: c.Binary,
	})
	if err != nil {
		return err
	}
	if err := appendUpdateJournal(updateJournalEntry{
		Event:   "completed",
		Version: version.Version,
		Changes: summary.OperationsChanged,
	}); err != nil {
		return err
	}
	fmt.Printf("box updated: %s (%d changes)\n", version.Version, summary.OperationsChanged)
	return nil
}

func writeVerifiedUpdateBinary(data []byte) (string, func(), error) {
	dir, err := os.MkdirTemp(host.DeployTmpDir(), "box-update-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create verified helper directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "helper")
	if err := os.WriteFile(path, data, 0755); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write verified helper: %w", err)
	}
	return path, cleanup, nil
}

func environmentMap() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			env[name] = value
		}
	}
	return env
}

func validUpdateBinary(path string) bool {
	clean := filepath.Clean(path)
	if clean != path || !strings.HasPrefix(clean, host.DeployTmpDir()+"/") || filepath.Base(clean) != "helper" {
		return false
	}
	return !strings.Contains(strings.TrimPrefix(clean, host.DeployTmpDir()+"/"), "../")
}

type updateJournalEntry struct {
	SchemaVersion int            `json:"schema_version"`
	Event         string         `json:"event"`
	At            string         `json:"at"`
	Version       string         `json:"version"`
	Changes       int            `json:"changes"`
	Key           string         `json:"key,omitempty"`
	Actor         *journalMember `json:"actor,omitempty"`
}

func appendUpdateJournal(entry updateJournalEntry) error {
	entry.SchemaVersion = 1
	entry.At = time.Now().UTC().Format(time.RFC3339Nano)
	path := store.Default().UpdatesJournalPath()
	return journal.Append(path, entry)
}

func lastCompletedUpdateVersion(path string) (string, error) {
	last := ""
	_, err := journal.Read(path, func(line []byte) error {
		var entry updateJournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		if entry.Event == "completed" {
			last = strings.TrimSpace(entry.Version)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return last, nil
}
