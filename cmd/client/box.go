package client

import (
	"encoding/json"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/names"
	"github.com/fprl/ship/internal/utils"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box ls deploy@example.com"}), 2)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppListCommand(jsonFlag), "app list failed")
	fmt.Print(out)
}

func CmdBoxAddKey(server, source string) {
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box add-key <github-user|key|path> deploy@example.com"}), 2)
	}
	input, err := resolveBoxAddKeySource(source)
	if err != nil {
		utils.DieError(err, 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdin := []byte(strings.Join(input.Keys, "\n") + "\n")
	stdout, stderr, code, err := runner.RunSSHWithStdin(server, serverKeyAddCommand(input.Comment), stdin)
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "")
		if remote.Coded != nil {
			writeRemoteStderr(stderr)
			utils.DieError(remote.Coded, 1)
		}
		detail := remote.Detail
		if detail == "" {
			detail = "add-key failed"
		}
		utils.DieError(operationError(detail, "ship box add-key "+source), 1)
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
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box rm " + app + " deploy@example.com --confirm " + app}), 2)
	}
	if confirm != app {
		utils.DieError(errcat.New(errcat.CodeBoxRmConfirmationRequired, errcat.Fields{"app": app}), 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	out := runSSHChecked(runner, server, serverAppDestroyCommand(app), "box rm failed")
	fmt.Print(out)
}

func CmdBoxDoctor(server string, jsonFlag bool) {
	if !config.ValidateSshTarget(server) {
		utils.DieError(errcat.New(errcat.CodeInvalidBoxTarget, errcat.Fields{"command": "ship box doctor deploy@example.com"}), 2)
	}

	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()

	stdout, stderr, code, err := runner.RunSSH(server, serverDoctorCommand(server, jsonFlag))
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "")
		if remote.Coded != nil {
			utils.DieError(remote.Coded, 1)
		}
		if jsonFlag && json.Valid([]byte(stdout)) {
			fmt.Print(stdout)
			os.Exit(1)
		}
		if remote.Detail != "" {
			utils.DieError(operationError(fmt.Sprintf("failed to run doctor: %s", remote.Detail), "ship box doctor "+server), 1)
		}
		utils.DieError(operationError("failed to run doctor", "ship box doctor "+server), 1)
	}
	fmt.Print(stdout)
}

type boxAddKeyInput struct {
	Comment string
	Keys    []string
}

var githubUserRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func resolveBoxAddKeySource(source string) (boxAddKeyInput, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return boxAddKeyInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source is empty"})
	}
	if strings.ContainsAny(source, "\r\n") {
		return boxAddKeyInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "key source must be a single key, GitHub user, or .pub path"})
	}
	if looksLikeSSHPublicKey(source) {
		keys, err := normalizeSSHPublicKeys(source, "")
		return boxAddKeyInput{Comment: keyComment(keys), Keys: keys}, err
	}
	if path, isPath := resolvePublicKeyPath(source); isPath {
		data, err := os.ReadFile(path)
		if err != nil {
			return boxAddKeyInput{}, operationError(fmt.Sprintf("read public key file %s: %v", source, err), "ship box add-key "+source)
		}
		comment := filepath.Base(path)
		keys, err := normalizeSSHPublicKeys(string(data), comment)
		return boxAddKeyInput{Comment: comment, Keys: keys}, err
	}
	if !githubUserRe.MatchString(source) {
		return boxAddKeyInput{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("%q is not a valid GitHub user, SSH public key, or .pub path", source)})
	}
	keys, err := fetchGitHubPublicKeys(source)
	if err != nil {
		return boxAddKeyInput{}, err
	}
	normalized, err := normalizeSSHPublicKeys(keys, source)
	return boxAddKeyInput{Comment: source, Keys: normalized}, err
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
	if strings.ContainsAny(source, `/\`) || strings.HasSuffix(source, ".pub") || strings.HasPrefix(source, ".") {
		return path, true
	}
	return "", false
}

func fetchGitHubPublicKeys(user string) (string, error) {
	url := "https://github.com/" + user + ".keys"
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", operationError(fmt.Sprintf("fetch %s: %v", url, err), "ship box add-key "+user)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errcat.New(errcat.CodeGitHubKeysUnavailable, errcat.Fields{"user": user})
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", operationError(fmt.Sprintf("fetch %s: HTTP %d", url, resp.StatusCode), "ship box add-key "+user)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", operationError(fmt.Sprintf("read %s: %v", url, err), "ship box add-key "+user)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errcat.New(errcat.CodeGitHubKeysUnavailable, errcat.Fields{"user": user})
	}
	return string(data), nil
}

func looksLikeSSHPublicKey(value string) bool {
	fields := strings.Fields(value)
	return len(fields) >= 2 && supportedPublicKeyType(fields[0])
}

func normalizeSSHPublicKeys(raw, comment string) ([]string, error) {
	var keys []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := normalizeSSHPublicKeyLine(line, comment)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "no SSH public keys found"})
	}
	return keys, nil
}

func normalizeSSHPublicKeyLine(line, forcedComment string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key line must contain key type and key body"})
	}
	if !supportedPublicKeyType(fields[0]) {
		return "", errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("unsupported public key type %q", fields[0])})
	}
	if fields[1] == "" {
		return "", errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key body is empty"})
	}
	comment := strings.TrimSpace(forcedComment)
	if comment == "" && len(fields) > 2 {
		comment = strings.Join(fields[2:], " ")
	}
	if comment == "" {
		comment = "ship-box-add-key"
	}
	comment = strings.Join(strings.Fields(comment), " ")
	return fields[0] + " " + fields[1] + " " + comment, nil
}

func supportedPublicKeyType(value string) bool {
	switch value {
	case "ssh-ed25519", "ssh-rsa",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return false
	}
}

func keyComment(keys []string) string {
	if len(keys) == 0 {
		return "ship-box-add-key"
	}
	fields := strings.Fields(keys[0])
	if len(fields) < 3 {
		return "ship-box-add-key"
	}
	return strings.Join(fields[2:], " ")
}
