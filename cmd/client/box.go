package client

import (
	"encoding/json"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/release"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/internal/version"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

func CmdBoxLs(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box ls"), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed", "ship box ls "+server)
	fmt.Print(out)
}

type boxVersionPayload struct {
	Version               string `json:"version"`
	RecordedClientVersion string `json:"recorded_client_version"`
	LastClientVersion     string `json:"last_client_version"`
	Architecture          string `json:"architecture"`
}

type boxStatusPayload struct {
	HelperVersion     string `json:"helper_version"`
	ClientVersion     string `json:"client_version"`
	LastClientVersion string `json:"last_client_version"`
	UpdateAvailable   bool   `json:"update_available"`
	HelperAhead       bool   `json:"helper_ahead"`
	Disk              struct {
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	} `json:"disk"`
	Apps             []boxStatusApp `json:"apps"`
	PendingApprovals int            `json:"pending_approvals"`
}

type boxStatusApp struct {
	App      string `json:"app"`
	EnvCount int    `json:"env_count"`
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
	fmt.Printf("helper: %s\nclient: %s\n", payload.HelperVersion, payload.ClientVersion)
	if payload.LastClientVersion != "" {
		fmt.Printf("last client: %s\n", payload.LastClientVersion)
	}
	if payload.UpdateAvailable {
		fmt.Printf("helper is behind\nnext: ship box update %s\n", server)
	} else if payload.HelperAhead {
		fmt.Println("client is behind helper")
	}
	fmt.Printf("disk: %s\n", payload.Disk.Evidence)
	if len(payload.Apps) == 0 {
		fmt.Println("apps: 0")
	} else {
		fmt.Println("apps:")
		for _, app := range payload.Apps {
			fmt.Printf("  %s: %d envs\n", app.App, app.EnvCount)
		}
	}
	fmt.Printf("pending approvals: %d\n", payload.PendingApprovals)
}

func readBoxStatus(runner *CommandRunner, server string) (boxStatusPayload, error) {
	var payload boxStatusPayload
	versionPayload, err := readBoxVersion(runner, server)
	if err != nil {
		return payload, err
	}
	payload.HelperVersion = versionPayload.Version
	payload.ClientVersion = version.Version
	payload.LastClientVersion = versionPayload.LastClientVersion
	cmp := compareShipVersions(versionPayload.Version, version.Version)
	payload.UpdateAvailable = cmp < 0
	payload.HelperAhead = cmp > 0 || (cmp == 0 && versionPayload.Version != version.Version)

	appsOut, err := runSSHDetail(runner, server, serverAppListCommand(true))
	if err != nil {
		return payload, err
	}
	var apps appListJSON
	if err := json.Unmarshal([]byte(appsOut), &apps); err != nil {
		return payload, operationError(fmt.Sprintf("box status failed: invalid app list JSON: %v", err), "ship box status "+server)
	}
	payload.Apps = make([]boxStatusApp, 0, len(apps.Apps))
	for _, app := range apps.Apps {
		payload.Apps = append(payload.Apps, boxStatusApp{App: app.App, EnvCount: len(app.Envs)})
	}

	payload.PendingApprovals, err = fetchPendingApprovalCount(runner, server)
	if err != nil {
		return payload, err
	}
	doctorOut, err := runBoxDoctorJSON(runner, server)
	if err != nil {
		return payload, err
	}
	var checks []struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(doctorOut), &checks); err != nil {
		return payload, operationError(fmt.Sprintf("box status failed: invalid doctor JSON: %v", err), "ship box status "+server)
	}
	for _, check := range checks {
		if check.ID == "disk_space" {
			payload.Disk.Status = check.Status
			payload.Disk.Evidence = check.Evidence
			break
		}
	}
	return payload, nil
}

// readBoxVersion probes the helper version. A box provisioned before the
// version/update verbs existed fails in one of two shapes — sudoers denies
// the verb, or an old helper rejects it as usage — and both mean the same
// thing: converge once with `ship box setup`, the day-0 and recovery path.
func readBoxVersion(runner sshRunner, server string) (boxVersionPayload, error) {
	stdout, stderr, code, err := runner.RunSSH(server, serverVersionCommand(true))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "box version probe failed")
		if outcome.TransportCoded != nil {
			return boxVersionPayload{}, outcome.TransportCoded
		}
		if outcome.RemoteCoded != nil {
			if outcome.RemoteCoded.Code() == errcat.CodeUsageError {
				return boxVersionPayload{}, errcat.New(errcat.CodeBoxSetupRequired, errcat.Fields{"server": server})
			}
			writeRemoteStderr(outcome)
			return boxVersionPayload{}, outcome.RemoteCoded
		}
		if strings.HasPrefix(outcome.Detail, "sudo:") {
			return boxVersionPayload{}, errcat.New(errcat.CodeBoxSetupRequired, errcat.Fields{"server": server})
		}
		return boxVersionPayload{}, operationError(outcome.Detail, "ship box status "+server)
	}
	var payload boxVersionPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil || payload.Version == "" {
		return boxVersionPayload{}, operationError("box status failed: invalid helper version JSON", "ship box status "+server)
	}
	return payload, nil
}

// Doctor intentionally exits non-zero when degraded. Status still consumes its
// JSON because disk evidence remains useful while a version mismatch exists.
func runBoxDoctorJSON(runner *CommandRunner, server string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(server, true))
	if json.Valid([]byte(stdout)) {
		return stdout, nil
	}
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "box doctor failed")
		if outcome.RemoteCoded != nil {
			return "", outcome.RemoteCoded
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
	cmp := compareShipVersions(remote.Version, version.Version)
	if err := validateBoxUpdateTarget(remote.Version, version.Version, server); err != nil {
		utils.DieError(err, 1)
	}
	if cmp == 0 {
		fmt.Println("box update: already current")
		return
	}
	stdout, stderr, code, err := runner.RunSSH(server, serverUpdateCommand(version.Version))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "box update failed")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		utils.DieError(operationError(outcome.Detail, "ship box update "+server), 1)
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
	cmp := compareShipVersions(helperVersion, clientVersion)
	if cmp > 0 {
		return errcat.New(errcat.CodeClientBehindHelper, errcat.Fields{"helper_version": helperVersion, "client_version": clientVersion})
	}
	if cmp == 0 && helperVersion != clientVersion {
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

func CmdMemberAdd(server, source string, role string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "fix ship.toml box"}), 2)
	}
	input, err := resolveMemberAddSource(source)
	if err != nil {
		utils.DieError(err, 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdin := []byte(strings.Join(input.Keys, "\n") + "\n")
	stdout, stderr, code, err := runner.RunSSHWithStdin(server, serverKeyAddCommand(input.Comment, role), stdin)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "member add failed"
		}
		utils.DieError(operationError(detail, "ship member add "+source), 1)
	}
	fmt.Print(stdout)
}

func CmdMemberLs(server string, jsonFlag bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "fix ship.toml box"}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverKeyListCommand(jsonFlag))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "member ls failed"
		}
		utils.DieError(operationError(detail, "ship member ls"), 1)
	}
	fmt.Print(stdout)
}

func CmdMemberRm(server, name string) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "fix ship.toml box"}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverKeyRmCommand(name))
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "member rm failed"
		}
		utils.DieError(operationError(detail, "ship member rm "+name), 1)
	}
	fmt.Print(stdout)
}

func CmdBoxRm(server, app, confirm string) {
	if !names.AppRe.MatchString(app) {
		utils.DieError(errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  fmt.Sprintf("invalid app name %q: must match %s", app, names.AppPattern),
			"command": "ship box rm <app> --confirm <app>",
		}), 2)
	}
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box rm "+app), 2)
	}
	if confirm != app {
		utils.DieError(errcat.New(errcat.CodeBoxRmConfirmationRequired, errcat.Fields{"app": app}), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppDestroyCommand(app), "box rm failed", "ship box rm "+server+" "+app+" --confirm "+app)
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
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
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

func CmdBoxNotify(server, url string, remove bool) {
	if !config.ValidateBoxHost(server) {
		utils.DieError(invalidBoxTargetError(server, "ship box notify"), 2)
	}
	if remove && strings.TrimSpace(url) != "" {
		utils.DieError(errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "--rm cannot be combined with a webhook URL",
			"command": "ship box notify <box> --rm",
		}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	command := serverBoxNotifyGetCommand()
	if remove {
		command = serverBoxNotifyClearCommand()
	} else if strings.TrimSpace(url) != "" {
		command = serverBoxNotifySetCommand(url)
	}
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		outcome := decodeRemoteOutcome(stdout, stderr, code, err, "")
		if outcome.TransportCoded != nil {
			utils.DieError(outcome.TransportCoded, 1)
		}
		if outcome.RemoteCoded != nil {
			writeRemoteStderr(outcome)
			utils.DieError(outcome.RemoteCoded, 1)
		}
		detail := outcome.Detail
		if detail == "" {
			detail = "box notify failed"
		}
		utils.DieError(operationError(detail, "ship box notify "+server), 1)
	}
	fmt.Print(stdout)
}

func invalidBoxTargetError(target string, prefix string) error {
	box := "203.0.113.7"
	if host, ok := config.UserHostBoxHost(target); ok {
		box = host
	}
	return errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": prefix + " " + box})
}

type memberAddInput struct {
	Comment string
	Keys    []string
}

var githubUserRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func resolveMemberAddSource(source string) (memberAddInput, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return memberAddInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source is empty"})
	}
	if strings.ContainsAny(source, "\r\n") {
		return memberAddInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source must be a single key, GitHub user, or .pub/.pem path"})
	}
	if looksLikeSSHPublicKey(source) {
		keys, err := normalizeSSHPublicKeys(source, "")
		return memberAddInput{Comment: keyComment(keys), Keys: keys}, err
	}
	if path, isPath := resolvePublicKeyPath(source); isPath {
		data, err := os.ReadFile(path)
		if err != nil {
			return memberAddInput{}, operationError(fmt.Sprintf("read public key file %s: %v", source, err), "ship member add "+source)
		}
		comment := memberNameFromPath(path)
		keys, err := normalizeSSHPublicKeys(string(data), comment)
		return memberAddInput{Comment: comment, Keys: keys}, err
	}
	if !githubUserRe.MatchString(source) {
		return memberAddInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("%q is not a valid GitHub user, SSH public key, or .pub/.pem path", source)})
	}
	keys, err := fetchGitHubPublicKeys(source)
	if err != nil {
		return memberAddInput{}, err
	}
	normalized, err := normalizeSSHPublicKeys(keys, source)
	return memberAddInput{Comment: source, Keys: normalized}, err
}

func memberNameFromPath(path string) string {
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	switch strings.ToLower(ext) {
	case ".pub", ".pem":
		return strings.TrimSuffix(name, ext)
	default:
		return name
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

func fetchGitHubPublicKeys(user string) (string, error) {
	url := "https://github.com/" + user + ".keys"
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", operationError(fmt.Sprintf("fetch %s: %v", url, err), "ship member add "+user)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errcat.New(errcat.CodeGitHubKeysUnavailable, errcat.Fields{"user": user})
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", operationError(fmt.Sprintf("fetch %s: HTTP %d", url, resp.StatusCode), "ship member add "+user)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", operationError(fmt.Sprintf("read %s: %v", url, err), "ship member add "+user)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errcat.New(errcat.CodeGitHubKeysUnavailable, errcat.Fields{"user": user})
	}
	return string(data), nil
}

func looksLikeSSHPublicKey(value string) bool {
	fields := strings.Fields(value)
	return len(fields) >= 2 && memberkeys.SupportedType(fields[0])
}

func normalizeSSHPublicKeys(raw, comment string) ([]string, error) {
	keys, err := memberkeys.Normalize(raw, comment)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key.Line)
	}
	return lines, nil
}

func keyComment(keys []string) string {
	if len(keys) == 0 {
		return "ship-member"
	}
	fields := strings.Fields(keys[0])
	if len(fields) < 3 {
		return "ship-member"
	}
	return strings.Join(fields[2:], " ")
}
