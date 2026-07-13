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
	Summary           bool   `name:"summary" hidden:"" help:"Include the compact box status summary."`
}

type boxStatusSummary struct {
	Disk struct {
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	} `json:"disk"`
	Apps             []boxStatusAppSummary `json:"apps"`
	PendingApprovals int                   `json:"pending_approvals"`
}

type boxStatusAppSummary struct {
	App      string `json:"app"`
	EnvCount int    `json:"env_count"`
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
	payload := struct {
		Version               string `json:"version"`
		RecordedClientVersion string `json:"recorded_client_version"`
		LastClientVersion     string `json:"last_client_version"`
		Architecture          string `json:"architecture"`
	}{Version: version.Version, RecordedClientVersion: recorded, LastClientVersion: lastClient, Architecture: runtime.GOARCH}
	if !c.Summary {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	summary, err := readBoxStatusSummary()
	if err != nil {
		utils.DieError(err, 1)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Version               string `json:"version"`
		RecordedClientVersion string `json:"recorded_client_version"`
		LastClientVersion     string `json:"last_client_version"`
		Architecture          string `json:"architecture"`
		Disk                  struct {
			Status   string `json:"status"`
			Evidence string `json:"evidence"`
		} `json:"disk"`
		Apps             []boxStatusAppSummary `json:"apps"`
		PendingApprovals int                   `json:"pending_approvals"`
	}{Version: payload.Version, RecordedClientVersion: payload.RecordedClientVersion, LastClientVersion: payload.LastClientVersion, Architecture: payload.Architecture, Disk: summary.Disk, Apps: summary.Apps, PendingApprovals: summary.PendingApprovals})
}

func readBoxStatusSummary() (boxStatusSummary, error) {
	var summary boxStatusSummary
	apps, err := appListStatuses()
	if err != nil {
		return summary, err
	}
	for _, app := range apps {
		if len(summary.Apps) == 0 || summary.Apps[len(summary.Apps)-1].App != app.App {
			summary.Apps = append(summary.Apps, boxStatusAppSummary{App: app.App})
		}
		summary.Apps[len(summary.Apps)-1].EnvCount++
	}
	disk := doctorDiskSpaceCheck(diskUsageForPath, "")
	summary.Disk.Status = disk.Status
	summary.Disk.Evidence = disk.Evidence
	pending, err := pendingApprovals()
	if err != nil {
		return summary, err
	}
	summary.PendingApprovals = len(pending)
	return summary, nil
}
