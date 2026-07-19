package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/release"
	"github.com/fprl/ship/internal/remoteprotocol"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func BoxTarget(root string) (string, error) {
	ctx, err := config.LoadAppContext(root, productionEnvName)
	if err != nil {
		return "", err
	}
	return ctx.Server, nil
}

func CmdBoxAppLs(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box app ls"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppLsCommand(jsonFlag), "app ls failed", "ship box app ls "+server)
	fmt.Print(out)
}

type boxVersionPayload = remoteprotocol.VersionResponse

type boxStatusSummaryPayload struct {
	Version     string `json:"version"`
	ShipVersion string `json:"ship_version"`
	Disk        struct {
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	} `json:"disk"`
	Apps             []boxStatusApp    `json:"apps"`
	Members          *boxStatusMembers `json:"members,omitempty"`
	PendingApprovals int               `json:"pending_approvals"`
	Doctor           *boxStatusDoctor  `json:"doctor,omitempty"`
}

type boxStatusPayload struct {
	HelperVersion   string `json:"helper_version"`
	ClientVersion   string `json:"client_version"`
	ShipVersion     string `json:"ship_version"`
	UpdateAvailable bool   `json:"update_available"`
	HelperAhead     bool   `json:"helper_ahead"`
	Disk            struct {
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	} `json:"disk"`
	Apps             []boxStatusApp    `json:"apps"`
	Members          *boxStatusMembers `json:"members,omitempty"`
	PendingApprovals int               `json:"pending_approvals"`
	Doctor           *boxStatusDoctor  `json:"doctor,omitempty"`
}

type boxStatusApp struct {
	App      string `json:"app"`
	EnvCount int    `json:"env_count"`
}

type boxStatusMembers struct {
	Total  int `json:"total"`
	Owners int `json:"owners"`
}

type boxStatusDoctor struct {
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
}

func CmdBoxStatus(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box status"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	payload, err := readBoxStatus(runner, server)
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(data))
		return
	}
	fmt.Print(renderBoxStatus(payload, server, time.Now()))
}

func CmdBoxGC(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box gc"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	out, err := runSSHDetail(runner, server, serverGCCommand(jsonFlag), "ship box gc "+server)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(out)
}

func renderBoxStatus(payload boxStatusPayload, server string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "helper: %s\nclient: %s\n", payload.HelperVersion, payload.ClientVersion)
	if payload.ShipVersion != "" {
		fmt.Fprintf(&b, "ship: %s\n", payload.ShipVersion)
	}
	if payload.UpdateAvailable {
		fmt.Fprintf(&b, "helper is behind\nnext: ship box update %s\n", server)
	} else if payload.HelperAhead {
		fmt.Fprintln(&b, "client is behind helper")
	}
	fmt.Fprintf(&b, "disk: %s\n", payload.Disk.Evidence)
	appCount := len(payload.Apps)
	if appCount == 0 {
		fmt.Fprintln(&b, "apps: 0")
	} else {
		envCount := 0
		for _, app := range payload.Apps {
			envCount += app.EnvCount
		}
		fmt.Fprintf(&b, "apps: %d (%d envs)\n", appCount, envCount)
	}
	if payload.Members == nil {
		fmt.Fprintln(&b, "members: unknown")
	} else {
		fmt.Fprintf(&b, "members: %d (%d owners)\n", payload.Members.Total, payload.Members.Owners)
	}
	fmt.Fprintf(&b, "pending approvals: %d\n", payload.PendingApprovals)
	if payload.Doctor == nil || payload.Doctor.RecordedAt == "" {
		fmt.Fprintln(&b, "doctor: never run")
		return b.String()
	}
	age := "just now"
	if recordedAt, err := time.Parse(time.RFC3339Nano, payload.Doctor.RecordedAt); err == nil {
		elapsed := now.Sub(recordedAt)
		switch {
		case elapsed >= 24*time.Hour:
			age = fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
		case elapsed >= time.Hour:
			age = fmt.Sprintf("%dh ago", int(elapsed.Hours()))
		case elapsed >= time.Minute:
			age = fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
		}
	}
	fmt.Fprintf(&b, "doctor: %s (%s)\n", payload.Doctor.Status, age)
	return b.String()
}

func readBoxStatus(runner sshRunner, server string) (boxStatusPayload, error) {
	var payload boxStatusPayload
	summary, err := readBoxStatusSummary(runner, server)
	if err != nil {
		return payload, err
	}
	payload.HelperVersion = summary.Version
	payload.ClientVersion = version.Version
	payload.ShipVersion = summary.ShipVersion
	cmp, ok := version.Compare(summary.Version, version.Version)
	payload.UpdateAvailable = ok && cmp < 0
	payload.HelperAhead = (ok && cmp > 0) || ((!ok || cmp == 0) && summary.Version != version.Version)

	payload.Disk = summary.Disk
	payload.Apps = summary.Apps
	payload.Members = summary.Members
	payload.PendingApprovals = summary.PendingApprovals
	payload.Doctor = summary.Doctor
	return payload, nil
}

// readBoxVersion probes the helper version. A box whose sudoers fragment
// denies the verb has never completed `ship box setup` — map that shape to
// the day-0 recovery path instead of surfacing a raw sudo error.
func readBoxVersion(runner sshRunner, server string) (boxVersionPayload, error) {
	stdout, err := runBoxReadCommand(runner, server, serverVersionCommand(true), "box version probe failed")
	if err != nil {
		return boxVersionPayload{}, err
	}
	var payload boxVersionPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Version == "" {
		return boxVersionPayload{}, operationError("box status failed: invalid helper version JSON", "ship box status "+server)
	}
	return payload, nil
}

func readBoxStatusSummary(runner sshRunner, server string) (boxStatusSummaryPayload, error) {
	stdout, err := runBoxReadCommand(runner, server, serverBoxStatusCommand(), "box status failed")
	if err != nil {
		return boxStatusSummaryPayload{}, err
	}
	var payload boxStatusSummaryPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Version == "" || payload.Apps == nil {
		return boxStatusSummaryPayload{}, operationError("box status failed: invalid helper summary JSON", "ship box status "+server)
	}
	return payload, nil
}

func runBoxReadCommand(runner sshRunner, server, command, fallback string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, fallback, server)
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			return "", outcome.RemoteCoded
		}
		if strings.HasPrefix(outcome.Detail, "sudo:") {
			return "", errcat.New(errcat.CodeBoxSetupRequired, errcat.Fields{"server": server})
		}
		return "", operationError(outcome.Detail, "ship box status "+server)
	}
	return stdout, nil
}

func CmdBoxUpdate(server string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box update"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	remote, err := readBoxVersion(runner, server)
	if err != nil {
		utils.DieError(err, 1)
	}
	cmp, ok := version.Compare(remote.Version, version.Version)
	if err := validateBoxUpdateTarget(remote.Version, version.Version, server); err != nil {
		utils.DieError(err, 1)
	}
	if !ok || cmp == 0 {
		fmt.Println("box update: already current")
		return
	}
	stdout, stderr, code, err := runner.RunSSH(server, serverUpdateCommand(version.Version))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "box update failed", server)
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		utils.DieError(operationError(outcome.Detail, "ship box update "+server), 1)
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
}

func validateBoxUpdateTarget(helperVersion, clientVersion, server string) error {
	if !release.IsVersion(clientVersion) {
		return errcat.New(errcat.CodeBoxVersionAmbiguous, errcat.Fields{
			"helper_version": helperVersion,
			"client_version": clientVersion,
			"server":         server,
		})
	}
	return classifyBoxUpdate(helperVersion, clientVersion, server)
}

func classifyBoxUpdate(helperVersion, clientVersion, server string) error {
	if isGitDescribeVersion(helperVersion) && isGitDescribeVersion(clientVersion) && helperVersion != clientVersion {
		return errcat.New(errcat.CodeBoxVersionAmbiguous, errcat.Fields{
			"helper_version": helperVersion,
			"client_version": clientVersion,
			"server":         server,
		})
	}
	cmp, ok := version.Compare(helperVersion, clientVersion)
	if ok && cmp > 0 {
		return errcat.New(errcat.CodeClientBehindHelper, errcat.Fields{"helper_version": helperVersion, "client_version": clientVersion})
	}
	if (!ok || cmp == 0) && helperVersion != clientVersion {
		return errcat.New(errcat.CodeBoxVersionAmbiguous, errcat.Fields{"helper_version": helperVersion, "client_version": clientVersion, "server": server})
	}
	return nil
}

func isGitDescribeVersion(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	_, prerelease, ok := strings.Cut(value, "-")
	if !ok {
		return false
	}
	parts := strings.Split(prerelease, "-")
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "g") || len(parts[1]) == 1 {
		return false
	}
	_, err := strconv.Atoi(parts[0])
	return err == nil
}

func CmdBoxMemberAdd(server, source, name, role, confirm string) error {
	if !config.ValidateBoxHost(server) {
		return invalidBoxTargetError(server, "ship box member add "+source)
	}
	return runBoxMemberAdd(server, source, name, role, confirm)
}

func runBoxMemberAdd(server, source, name, role, confirm string) error {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--name is required for box member add",
			"command": memberAddCommand(source, server, name, role),
		})
	}
	if role != "owner" && role != "shipper" && role != "agent" {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "role must be owner, shipper, or agent",
			"command": "ship box member add <https-url|key|path> " + server + " --name " + utils.ShellEscape(name) + " --role owner|shipper|agent",
		})
	}
	parsedConfirm, err := parseMemberConfirm(confirm, source, server, name, role)
	if err != nil {
		return err
	}
	remote, err := isHTTPSMemberSource(source, server, name, role)
	if err != nil {
		return err
	}
	if parsedConfirm != "" && !remote {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--confirm is only valid with an HTTPS keys-URL; literal keys and files write immediately",
			"command": memberAddCommand(source, server, name, role),
		})
	}

	input, err := resolveMemberAddSource(server, source, name, role)
	if err != nil {
		return err
	}
	if remote {
		plan := newMemberAddPlan(server, source, input, name, role)
		if parsedConfirm == "" {
			fmt.Print(renderMemberAddPlan(plan))
			return nil
		}
		if parsedConfirm != name+"@sha256:"+plan.Digest {
			fmt.Print(renderMemberAddPlan(plan))
			return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
				"detail":  "member add confirmation does not match the freshly fetched plan",
				"command": memberAddCommitCommand(source, server, name, role, plan.Digest),
			})
		}
	}
	runner, err := NewCommandRunner()
	if err != nil {
		return err
	}
	defer runner.Close()

	stdin := []byte(strings.Join(input.Keys, "\n") + "\n")
	stdout, stderr, code, err := runner.RunSSHWithStdin(server, serverKeyAddCommand(name, role), stdin)
	if err != nil || code != 0 {
		return sshResultError(server, stdout, stderr, code, err, "", "member add failed", "ship box member add "+source+" "+server)
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
	return nil
}

func CmdBoxMemberLs(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box member ls"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverKeyListCommand(jsonFlag))
	if err != nil || code != 0 {
		if err := sshResultError(server, stdout, stderr, code, err, "", "box member ls failed", "ship box member ls "+server); err != nil {
			utils.DieError(err, 1)
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
}

func CmdBoxMemberRm(server, name, key string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box member rm "+name), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverKeyRmCommand(name, key))
	if err != nil || code != 0 {
		command := "ship box member rm " + name
		if key != "" {
			command += " --key " + key
		}
		if err := sshResultError(server, stdout, stderr, code, err, "", "member rm failed", command+" "+server); err != nil {
			utils.DieError(err, 1)
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
}

func CmdBoxMemberRename(server, oldName, newName string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box member rename "+oldName+" "+newName), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	stdout, stderr, code, err := runner.RunSSH(server, serverKeyRenameCommand(oldName, newName))
	if err != nil || code != 0 {
		if err := sshResultError(server, stdout, stderr, code, err, "", "member rename failed", "ship box member rename "+oldName+" "+newName+" "+server); err != nil {
			utils.DieError(err, 1)
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
}

func CmdBoxMemberRole(server, name, role string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box member role "+name+" "+role), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	stdout, stderr, code, err := runner.RunSSH(server, serverKeyRoleCommand(name, role))
	if err != nil || code != 0 {
		if err := sshResultError(server, stdout, stderr, code, err, "", "member role failed", "ship box member role "+name+" "+role+" "+server); err != nil {
			utils.DieError(err, 1)
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	fmt.Print(stdout)
}

func CmdBoxAppRm(server, app, confirm string) {
	if !names.AppRe.MatchString(app) {
		utils.DieError(errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  fmt.Sprintf("invalid app name %q: must match %s", app, names.AppPattern),
			"command": "ship box app rm " + utils.ShellEscape(app) + " " + utils.ShellEscape(server) + " --confirm " + utils.ShellEscape(app),
		}), 2)
	}
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box app rm "+app), 2)
	}
	if confirm != app {
		utils.DieError(errcat.New(errcat.CodeBoxAppRmConfirmationRequired, errcat.Fields{"app": app, "box": server}), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppDestroyCommand(app), "box app rm failed", "ship box app rm "+app+" "+server+" --confirm "+app)
	fmt.Print(out)
}

func CmdBoxForget(server string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box forget"), 2)
	}
	removed, err := knownhosts.Remove(server)
	if err != nil {
		utils.DieError(operationError(fmt.Sprintf("forget box host key: %v", err), "ship box forget "+server), 1)
	}
	if removed {
		fmt.Printf("forgot box %s (%s)\n", server, knownhosts.DisplayPath)
		return
	}
	fmt.Printf("box %s is not pinned (%s)\n", server, knownhosts.DisplayPath)
}

func CmdBoxDoctor(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box doctor"), 2)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(server, jsonFlag))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "", server)
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			utils.DieError(outcome.RemoteCoded, 1)
		}
		if jsonFlag && json.Valid([]byte(stdout)) {
			fmt.Print(stdout)
			os.Exit(1)
		}
		if outcome.Detail != "" {
			utils.DieError(operationError(fmt.Sprintf("failed to run doctor: %s", outcome.Detail), "ship box doctor "+server), 1)
		}
		utils.DieError(operationError("failed to run doctor", "ship box doctor "+server), 1)
	}
	fmt.Print(stdout)
}

func CmdBoxWebhook(server, url string, remove, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box webhook"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, err := runBoxWebhook(runner, server, url, remove, jsonFlag)
	if err != nil {
		if errcat.Is(err, errcat.CodeUsageError) {
			utils.DieError(err, 2)
		}
		utils.DieError(err, 1)
	}
	fmt.Print(stdout)
}

func runBoxWebhook(runner sshRunner, server, url string, remove, jsonFlag bool) (string, error) {
	if remove && strings.TrimSpace(url) != "" {
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--rm cannot be combined with a webhook URL",
			"command": "ship box webhook <box> --rm",
		})
	}
	if jsonFlag && (remove || strings.TrimSpace(url) != "") {
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--json is only valid when reading box webhook",
			"command": "ship box webhook <box> --json",
		})
	}
	if jsonFlag {
		payload, err := readBoxConfig(runner, server)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(struct {
			URL string `json:"url"`
		}{URL: payload.Config["webhook.url"].Value})
		if err != nil {
			return "", err
		}
		return string(data) + "\n", nil
	}

	command := serverBoxWebhookGetCommand()
	if remove {
		command = serverBoxWebhookClearCommand()
	} else if strings.TrimSpace(url) != "" {
		command = serverBoxWebhookSetCommand(url)
	}
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "", server)
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			return "", outcome.RemoteCoded
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "box webhook failed"
		}
		return "", operationError(detail, "ship box webhook "+server)
	}
	forwardStderr := strings.TrimSpace(stderr) != ""
	if _, stderrIsErrorJSON := errcat.ParseJSON(stderr); stderrIsErrorJSON {
		forwardStderr = false
	}
	writeRemoteStderr(remoteOutcome{Stderr: stderr, ForwardStderr: forwardStderr})
	return stdout, nil
}

type boxConfigValue struct {
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"`
}

type boxConfigPayload struct {
	Config map[string]boxConfigValue `json:"config"`
}

func CmdBoxConfigGet(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box config"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	payload, err := readBoxConfig(runner, server)
	if err != nil {
		utils.DieError(err, 1)
	}
	if jsonFlag {
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
		}
		fmt.Println(string(data))
		return
	}
	keys := make([]string, 0, len(payload.Config))
	keyWidth, valueWidth, defaultWidth := len("key"), len("value"), len("default")
	for key, value := range payload.Config {
		keys = append(keys, key)
		keyWidth = max(keyWidth, len(key))
		valueWidth = max(valueWidth, len(boxConfigDisplayValue(value.Value)))
		defaultWidth = max(defaultWidth, len(boxConfigDisplayValue(value.Default)))
	}
	sort.Strings(keys)
	fmt.Printf("%-*s  %-*s  %-*s  source\n", keyWidth, "key", valueWidth, "value", defaultWidth, "default")
	for _, key := range keys {
		value := payload.Config[key]
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", keyWidth, key, valueWidth, boxConfigDisplayValue(value.Value), defaultWidth, boxConfigDisplayValue(value.Default), value.Source)
	}
}

func CmdBoxConfigSet(server, key, value string) {
	CmdBoxConfigMutation(server, serverBoxConfigSetCommand(key, value), "ship box config "+server+" set "+key)
}

func CmdBoxConfigUnset(server, key string) {
	CmdBoxConfigMutation(server, serverBoxConfigUnsetCommand(key), "ship box config "+server+" unset "+key)
}

func CmdBoxConfigMutation(server, command, remediation string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box config"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	stdout, err := runBoxConfigMutation(runner, server, command, remediation)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Print(stdout)
}

func runBoxConfigMutation(runner sshRunner, server, command, remediation string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "", server)
		if outcome.TransportCoded != nil {
			return "", outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			return "", outcome.RemoteCoded
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "box config failed"
		}
		return "", operationError(detail, remediation)
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return stdout, nil
}

func readBoxConfig(runner sshRunner, server string) (boxConfigPayload, error) {
	out, err := runSSHDetail(runner, server, serverBoxConfigGetCommand(), "ship box config "+server)
	if err != nil {
		return boxConfigPayload{}, err
	}
	var payload boxConfigPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return boxConfigPayload{}, operationError(fmt.Sprintf("box config failed: invalid config JSON: %v", err), "ship box config "+server)
	}
	if payload.Config == nil {
		return boxConfigPayload{}, operationError("box config failed: invalid config JSON", "ship box config "+server)
	}
	return payload, nil
}

func boxConfigDisplayValue(value string) string {
	if value == "" {
		return "unset"
	}
	return value
}

const invalidBoxTargetManifestRemediation = "fix ship.toml box"

func invalidBoxTargetError(target string, prefix string) error {
	if prefix == invalidBoxTargetManifestRemediation {
		return errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": prefix})
	}
	box := "203.0.113.7"
	if host, ok := config.UserHostBoxHost(target); ok {
		box = host
	}
	return errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": prefix + " " + box})
}

type memberAddInput struct {
	Keys     []string
	FinalURL string
}

const (
	memberFetchTimeout    = 10 * time.Second
	memberMaxResponseSize = 1 << 20
	memberMaxKeyCount     = 64
	memberMaxRedirects    = 10
)

var memberURLFetcher = fetchHTTPSMemberKeys
var memberURLTransport http.RoundTripper

func resolveMemberAddSource(server, source, name, role string) (memberAddInput, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return memberAddInput{}, memberAddSourceError(errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source is empty"}), source, server, name, role)
	}
	if strings.ContainsAny(source, "\r\n") {
		return memberAddInput{}, memberAddSourceError(errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source must be a single key or .pub/.pem path"}), source, server, name, role)
	}
	if looksLikeSSHPublicKey(source) {
		keys, err := normalizeSSHPublicKeys(source)
		if err != nil {
			return memberAddInput{}, memberAddSourceError(err, source, server, name, role)
		}
		return memberAddInput{Keys: canonicalMemberKeyLines(keys)}, nil
	}
	remote, err := isHTTPSMemberSource(source, server, name, role)
	if err != nil {
		return memberAddInput{}, err
	}
	if remote {
		input, err := memberURLFetcher(source, server, name, role)
		if err != nil {
			return memberAddInput{}, memberAddSourceError(err, source, server, name, role)
		}
		return input, nil
	}
	if path, isPath := resolvePublicKeyPath(source); isPath {
		data, err := os.ReadFile(path)
		if err != nil {
			return memberAddInput{}, memberAddSourceError(fmt.Errorf("read public key file %s: %v", source, err), source, server, name, role)
		}
		keys, err := normalizeSSHPublicKeys(string(data))
		if err != nil {
			return memberAddInput{}, memberAddSourceError(err, source, server, name, role)
		}
		return memberAddInput{Keys: canonicalMemberKeyLines(keys)}, nil
	}
	return memberAddInput{}, remoteMemberSourceUsageError(source, server, name, role)
}

func isHTTPSMemberSource(source, server, name, role string) (bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), "http") {
			return false, remoteMemberSourceUsageError(source, server, name, role)
		}
		return false, nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return false, remoteMemberSourceUsageError(source, server, name, role)
		}
		return true, nil
	case "http":
		return false, remoteMemberSourceUsageError(source, server, name, role)
	default:
		return false, nil
	}
}

func resolvePublicKeyPath(source string) (string, bool) {
	path := source
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, true
	}
	lower := strings.ToLower(source)
	if strings.ContainsAny(source, `/\`) || strings.HasSuffix(lower, ".pub") || strings.HasSuffix(lower, ".pem") || strings.HasPrefix(source, ".") {
		return path, true
	}
	return "", false
}

func fetchHTTPSMemberKeys(source, server, name, role string) (memberAddInput, error) {
	parsed, err := url.Parse(source)
	if err != nil || strings.ToLower(parsed.Scheme) != "https" || parsed.Host == "" {
		return memberAddInput{}, remoteMemberSourceUsageError(source, server, name, role)
	}
	client := http.Client{
		Timeout: memberFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= memberMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if strings.ToLower(req.URL.Scheme) != "https" {
				return fmt.Errorf("insecure HTTP redirect is not allowed")
			}
			return nil
		},
	}
	if memberURLTransport != nil {
		client.Transport = memberURLTransport
	}
	resp, err := client.Get(source)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("fetch %s: %v", source, err), source, server, name, role)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return memberAddInput{}, memberAddSourceError(errcat.New(errcat.CodeKeysURLUnavailable, errcat.Fields{"source": source}), source, server, name, role)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("fetch %s: HTTP %d", source, resp.StatusCode), source, server, name, role)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, memberMaxResponseSize+1))
	if err != nil {
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("read %s: %v", source, err), source, server, name, role)
	}
	if len(data) > memberMaxResponseSize {
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("response from %s exceeds %d bytes", source, memberMaxResponseSize), source, server, name, role)
	}
	keys, err := memberkeys.Normalize(string(data), "")
	if err != nil {
		return memberAddInput{}, memberAddSourceError(err, source, server, name, role)
	}
	if len(keys) > memberMaxKeyCount {
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("response from %s contains more than %d keys", source, memberMaxKeyCount), source, server, name, role)
	}
	finalURL := source
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	finalParsed, err := url.Parse(finalURL)
	if err != nil || strings.ToLower(finalParsed.Scheme) != "https" {
		return memberAddInput{}, memberAddSourceError(fmt.Errorf("final URL for %s was not HTTPS", source), source, server, name, role)
	}
	return memberAddInput{
		Keys:     canonicalMemberKeyLinesFromAuthorized(keys),
		FinalURL: finalURL,
	}, nil
}

func looksLikeSSHPublicKey(value string) bool {
	fields := strings.Fields(value)
	return len(fields) >= 2 && memberkeys.SupportedType(fields[0])
}

func normalizeSSHPublicKeys(raw string) ([]string, error) {
	keys, err := memberkeys.Normalize(raw, "")
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key.Line)
	}
	return lines, nil
}

func canonicalMemberKeyLines(lines []string) []string {
	keys, err := memberkeys.Normalize(strings.Join(lines, "\n"), "")
	if err != nil {
		return lines
	}
	return canonicalMemberKeyLinesFromAuthorized(keys)
}

func canonicalMemberKeyLinesFromAuthorized(keys []memberkeys.AuthorizedKey) []string {
	seen := map[string]bool{}
	canonical := make([]string, 0, len(keys))
	for _, key := range keys {
		material := key.Type + " " + key.Body
		if seen[material] {
			continue
		}
		seen[material] = true
		canonical = append(canonical, material)
	}
	sort.Strings(canonical)
	return canonical
}

func remoteMemberSourceUsageError(source, server, name, role string) error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  fmt.Sprintf("%q is not an SSH public key, a key file path, or an HTTPS keys-URL; for a forge account use its keys URL, e.g. https://github.com/<user>.keys.", source),
		"command": memberAddCommand(source, server, name, role),
	})
}

func parseMemberConfirm(confirm, source, server, name, role string) (string, error) {
	confirm = strings.TrimSpace(confirm)
	if confirm == "" {
		return "", nil
	}
	confirmedName, digest, ok := strings.Cut(confirm, "@sha256:")
	if !ok || strings.TrimSpace(confirmedName) == "" || len(digest) != sha256.Size*2 {
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--confirm must match <name>@sha256:<plan-digest>",
			"command": memberAddCommand(source, server, name, role) + " --confirm <name>@sha256:<plan-digest>",
		})
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--confirm must match <name>@sha256:<plan-digest>",
			"command": memberAddCommand(source, server, name, role) + " --confirm <name>@sha256:<plan-digest>",
		})
	}
	return strings.Join(strings.Fields(confirmedName), " ") + "@sha256:" + digest, nil
}

type memberAddPlan struct {
	Box       string
	SourceURL string
	FinalURL  string
	Name      string
	Role      string
	Keys      []memberkeys.AuthorizedKey
	Digest    string
}

func newMemberAddPlan(box, source string, input memberAddInput, name, role string) memberAddPlan {
	keys, _ := memberkeys.Normalize(strings.Join(input.Keys, "\n"), "")
	plan := memberAddPlan{
		Box:       box,
		SourceURL: source,
		FinalURL:  input.FinalURL,
		Name:      name,
		Role:      role,
		Keys:      keys,
	}
	plan.Digest = digestMemberAddPlan(plan)
	return plan
}

func digestMemberAddPlan(plan memberAddPlan) string {
	materials := canonicalMemberKeyLinesFromAuthorized(plan.Keys)
	var encoded strings.Builder
	encoded.WriteString("ship-member-plan-v1\n")
	for _, field := range []string{plan.Box, plan.SourceURL, plan.FinalURL, plan.Name, plan.Role} {
		bytes := []byte(field)
		fmt.Fprintf(&encoded, "%d:", len(bytes))
		encoded.Write(bytes)
		encoded.WriteByte('\n')
	}
	fmt.Fprintf(&encoded, "%d\n", len(materials))
	for _, material := range materials {
		bytes := []byte(material)
		fmt.Fprintf(&encoded, "%d:", len(bytes))
		encoded.Write(bytes)
		encoded.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(encoded.String()))
	return hex.EncodeToString(sum[:])
}

func renderMemberAddPlan(plan memberAddPlan) string {
	var out strings.Builder
	fmt.Fprintf(&out, "source: %s\n", plan.SourceURL)
	fmt.Fprintf(&out, "final source: %s\n", plan.FinalURL)
	fmt.Fprintf(&out, "name: %s\nrole: %s\n", plan.Name, plan.Role)
	for index, key := range plan.Keys {
		fmt.Fprintf(&out, "key %d: %s\n", index+1, key.Type)
		fmt.Fprintf(&out, "  material: %s\n", key.Type+" "+key.Body)
		fmt.Fprintf(&out, "  fingerprint: %s\n", key.Fingerprint)
	}
	fmt.Fprintf(&out, "plan digest: sha256:%s\n", plan.Digest)
	fmt.Fprintf(&out, "next: %s\n", memberAddCommitCommand(plan.SourceURL, plan.Box, plan.Name, plan.Role, plan.Digest))
	return out.String()
}

func memberAddCommitCommand(source, box, name, role, digest string) string {
	return "ship box member add " + utils.ShellEscape(source) + " " + utils.ShellEscape(box) +
		" --name " + utils.ShellEscape(name) + " --role " + utils.ShellEscape(role) +
		" --confirm " + utils.ShellEscape(name+"@sha256:"+digest)
}

func memberAddCommand(source, box, name, role string) string {
	renderedName := "<name>"
	if name != "" {
		renderedName = utils.ShellEscape(name)
	}
	command := "ship box member add " + utils.ShellEscape(source) + " " + utils.ShellEscape(box) + " --name " + renderedName
	if role != "" && role != "shipper" {
		command += " --role " + utils.ShellEscape(role)
	}
	return command
}

func memberAddSourceError(err error, source, server, name, role string) error {
	command := memberAddCommand(source, server, name, role)
	if coded, ok := errcat.As(err); ok {
		fields := errcat.Fields{
			"detail":  coded.Cause(),
			"source":  source,
			"box":     server,
			"name":    name,
			"command": command,
		}
		switch coded.Code() {
		case errcat.CodeKeysURLUnavailable:
			return errcat.New(errcat.CodeKeysURLUnavailable, fields)
		case errcat.CodeSSHPublicKeyInvalid:
			return errcat.New(errcat.CodeSSHPublicKeyInvalid, fields)
		case errcat.CodeUsageError:
			return errcat.New(errcat.CodeUsageError, fields)
		case errcat.CodeOperationFailed:
			return errcat.New(errcat.CodeOperationFailed, fields)
		default:
			return operationError(coded.Cause(), command)
		}
	}
	return operationError(err.Error(), command)
}
