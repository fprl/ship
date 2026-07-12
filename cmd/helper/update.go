package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/provision"
	"github.com/fprl/ship/internal/provision/local"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

type updateHelperCmd struct {
	MemberFingerprint string `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Binary            string `name:"binary" required:"" help:"Uploaded client-matched helper binary."`
}

func (c updateHelperCmd) BeforeApply() error { return requireRoot() }

func (c updateHelperCmd) Run() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	if _, err := authorizeHelper(helperVerbBoxMutation, authTargetForBox("box update")); err != nil {
		utils.DieError(err, 1)
	}
	if !validUpdateBinary(c.Binary) {
		return fmt.Errorf("invalid update helper path: %s", c.Binary)
	}
	cmd := exec.Command(c.Binary, "server", "update-local", "--binary", c.Binary)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// updateLocalCmd is invoked by the uploaded binary after the installed helper
// has checked the member role. Keeping artifact rendering in the uploaded
// binary means an update always writes this client's owned files.
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
	if err := appendUpdateJournal(summary); err != nil {
		return err
	}
	fmt.Printf("box updated: %s (%d changes)\n", version.Version, summary.OperationsChanged)
	return nil
}

func validUpdateBinary(path string) bool {
	clean := filepath.Clean(path)
	if clean != path || !strings.HasPrefix(clean, host.DeployTmpDir()+"/") || filepath.Base(clean) != "helper" {
		return false
	}
	return !strings.Contains(strings.TrimPrefix(clean, host.DeployTmpDir()+"/"), "../")
}

type updateJournalEntry struct {
	SchemaVersion int    `json:"schema_version"`
	At            string `json:"at"`
	Version       string `json:"version"`
	Changes       int    `json:"changes"`
}

func appendUpdateJournal(summary provision.InstallSummary) error {
	entry, err := json.Marshal(updateJournalEntry{
		SchemaVersion: 1,
		At:            time.Now().UTC().Format(time.RFC3339Nano),
		Version:       version.Version,
		Changes:       summary.OperationsChanged,
	})
	if err != nil {
		return err
	}
	path := store.Default().UpdatesJournalPath()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(entry, '\n'))
	return err
}
