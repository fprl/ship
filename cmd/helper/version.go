package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
)

// versionHelperCmd is deliberately a read verb: every enrolled member needs
// to be able to see whether a box is behind their client.
type versionHelperCmd struct {
	MemberFingerprint string `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	JSON              bool   `name:"json" help:"Emit version metadata as JSON."`
}

func (c versionHelperCmd) BeforeApply() error { return requireRoot() }

func (c versionHelperCmd) Run() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	if _, err := authorizeHelper(helperVerbRead, authTargetForBox("box version")); err != nil {
		utils.DieError(err, 1)
	}
	if !c.JSON {
		fmt.Println(version.Version)
		return nil
	}
	recorded := ""
	lastClient := ""
	if hostFile, err := store.Default().ReadHost(); err == nil {
		recorded = strings.TrimSpace(hostFile.Meta.ShipVersion)
		lastClient = strings.TrimSpace(hostFile.Meta.LastClientVersion)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Version               string `json:"version"`
		RecordedClientVersion string `json:"recorded_client_version"`
		LastClientVersion     string `json:"last_client_version"`
		Architecture          string `json:"architecture"`
	}{Version: version.Version, RecordedClientVersion: recorded, LastClientVersion: lastClient, Architecture: runtime.GOARCH})
}
