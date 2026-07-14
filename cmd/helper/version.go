package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	Apps             []boxStatusAppSummary   `json:"apps"`
	PendingApprovals int                     `json:"pending_approvals"`
	Doctor           *boxStatusDoctorSummary `json:"doctor,omitempty"`
}

type boxStatusAppSummary struct {
	App      string `json:"app"`
	EnvCount int    `json:"env_count"`
}

type boxStatusDoctorSummary struct {
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
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
	lastClient := ""
	if hostFile, err := store.Default().ReadHost(); err == nil {
		lastClient = strings.TrimSpace(hostFile.Meta.LastClientVersion)
	}
	payload := struct {
		Version           string `json:"version"`
		LastClientVersion string `json:"last_client_version"`
		Architecture      string `json:"architecture"`
	}{Version: version.Version, LastClientVersion: lastClient, Architecture: runtime.GOARCH}
	if !c.Summary {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	summary, err := readBoxStatusSummary()
	if err != nil {
		utils.DieError(err, 1)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Version           string `json:"version"`
		LastClientVersion string `json:"last_client_version"`
		Architecture      string `json:"architecture"`
		Disk              struct {
			Status   string `json:"status"`
			Evidence string `json:"evidence"`
		} `json:"disk"`
		Apps             []boxStatusAppSummary   `json:"apps"`
		PendingApprovals int                     `json:"pending_approvals"`
		Doctor           *boxStatusDoctorSummary `json:"doctor,omitempty"`
	}{Version: payload.Version, LastClientVersion: payload.LastClientVersion, Architecture: payload.Architecture, Disk: summary.Disk, Apps: summary.Apps, PendingApprovals: summary.PendingApprovals, Doctor: summary.Doctor})
}

func readBoxStatusSummary() (boxStatusSummary, error) {
	summary := boxStatusSummary{Apps: []boxStatusAppSummary{}}
	paths, err := filepath.Glob(identityGlob())
	if err != nil {
		return summary, err
	}
	appEnvCounts := make(map[string]int)
	for _, path := range paths {
		file, err := readEnvIdentityFile(path)
		if err != nil {
			continue
		}
		appEnvCounts[file.App]++
	}
	for app, envCount := range appEnvCounts {
		summary.Apps = append(summary.Apps, boxStatusAppSummary{App: app, EnvCount: envCount})
	}
	sort.Slice(summary.Apps, func(i, j int) bool { return summary.Apps[i].App < summary.Apps[j].App })
	disk := doctorDiskSpaceCheck(diskUsageForPath, "")
	summary.Disk.Status = disk.Status
	summary.Disk.Evidence = disk.Evidence
	pending, err := pendingApprovals()
	if err != nil {
		return summary, err
	}
	summary.PendingApprovals = len(pending)
	doctor, err := store.Default().ReadDoctor()
	if err == nil {
		status := doctorStatusDegraded
		if doctorChecksOK(doctor.Checks) {
			status = doctorStatusOK
		}
		summary.Doctor = &boxStatusDoctorSummary{Status: status, RecordedAt: doctor.RecordedAt}
	} else if !os.IsNotExist(err) {
		return summary, err
	}
	return summary, nil
}
