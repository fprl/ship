package client

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/utils"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CommandRunner struct {
	SshOptions       []string
	RsyncRemoteShell string
	TempDir          string
}

func NewCommandRunner() (*CommandRunner, error) {
	sshOpts := []string{"-o", "BatchMode=yes"}
	key := os.Getenv("SHIP_SSH_KEY")
	if key == "" {
		return &CommandRunner{
			SshOptions:       sshOpts,
			RsyncRemoteShell: sshRemoteShell(sshOpts),
		}, nil
	}
	dir, err := os.MkdirTemp("", "ship-ssh-")
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "id")

	ensureNL := func(s string) string {
		if !strings.HasSuffix(s, "\n") {
			return s + "\n"
		}
		return s
	}

	if err := os.WriteFile(keyPath, []byte(ensureNL(key)), 0600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	sshOpts = append(sshOpts,
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
	)

	return &CommandRunner{
		SshOptions:       sshOpts,
		RsyncRemoteShell: sshRemoteShell(sshOpts),
		TempDir:          dir,
	}, nil
}

func sshRemoteShell(sshOpts []string) string {
	if len(sshOpts) == 0 {
		return ""
	}
	var escOpts []string
	for _, opt := range sshOpts {
		escOpts = append(escOpts, utils.ShellEscape(opt))
	}
	return "ssh " + strings.Join(escOpts, " ")
}

func (r *CommandRunner) Close() {
	if r.TempDir != "" {
		_ = os.RemoveAll(r.TempDir)
	}
}

func deployIdentity(root string, runner *CommandRunner, server string) deployIdentityJSON {
	actor := deployIdentityJSON{
		SSHKeyComment: sshKeyCommentForServer(runner, server),
		GitAuthor:     gitAuthor(root),
	}
	if actor.SSHKeyComment == "" {
		actor.SSHKeyComment = "unknown"
	}
	if actor.GitAuthor == "" {
		actor.GitAuthor = "unknown"
	}
	return actor
}

func gitAuthor(root string) string {
	nameOut, _, nameCode, _ := runCommand("git", []string{"config", "user.name"}, root)
	emailOut, _, emailCode, _ := runCommand("git", []string{"config", "user.email"}, root)
	name := strings.TrimSpace(nameOut)
	email := strings.TrimSpace(emailOut)
	switch {
	case nameCode == 0 && emailCode == 0 && name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case nameCode == 0 && name != "":
		return name
	case emailCode == 0 && email != "":
		return email
	default:
		out, _, code, _ := runCommand("git", []string{"log", "-1", "--format=%an <%ae>"}, root)
		if code == 0 {
			return strings.TrimSpace(out)
		}
		return ""
	}
}

func sshKeyCommentForServer(runner *CommandRunner, server string) string {
	var args []string
	args = append(args, runner.SshOptions...)
	args = append(args, "-G", server)
	stdout, _, code, _ := runCommand("ssh", args, "")
	if code != 0 {
		return ""
	}
	for _, path := range sshIdentityFiles(stdout) {
		if comment := publicKeyComment(path + ".pub"); comment != "" {
			return comment
		}
	}
	return ""
}

func sshIdentityFiles(sshConfig string) []string {
	var out []string
	for _, line := range strings.Split(sshConfig, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "identityfile" || fields[1] == "none" {
			continue
		}
		out = append(out, fields[1])
	}
	return out
}

func publicKeyComment(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return ""
	}
	prefix := parts[0] + " " + parts[1]
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

func (r *CommandRunner) RunSSH(server string, command string) (string, string, int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	args = append(args, server, command)
	return runCommand("ssh", args, "")
}

// RunSSHWithStdin pipes `stdin` to the remote command and captures
// stdout/stderr/exit. Used by `ship secret set` so the secret
// value never lands in argv, the host process table, or shell
// history — it crosses the wire on the helper's stdin and goes
// straight to disk on the other side.
func (r *CommandRunner) RunSSHWithStdin(server string, command string, stdin []byte) (string, string, int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	args = append(args, server, command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func (r *CommandRunner) RunSSHPassthrough(server string, command string) error {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	if command != "" {
		args = append(args, server, command)
	} else {
		args = append(args, server)
	}
	return runCommandPassthrough("ssh", args)
}

func (r *CommandRunner) RunSSHPassthroughExitCode(server string, command string, tty bool) (int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	if tty {
		args = append(args, "-tt")
	}
	args = append(args, server, command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

func (r *CommandRunner) Upload(local string, remote string, server string) error {
	var args []string
	if r.RsyncRemoteShell != "" {
		args = append(args, "-e", r.RsyncRemoteShell)
	}
	args = append(args, "-az", local, fmt.Sprintf("%s:%s", server, remote))
	_, stderr, code, err := runCommand("rsync", args, "")
	if err != nil || code != 0 {
		return operationError(fmt.Sprintf("rsync failed (exit %d): %s", code, strings.TrimSpace(stderr)), "ship")
	}
	return nil
}

func runCommand(name string, args []string, dir string) (string, string, int, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return stdout.String(), stderr.String(), code, err
}

func runCommandPassthrough(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type sshRunner interface {
	RunSSH(server string, command string) (string, string, int, error)
}

func runSSHRequired(runner sshRunner, server string, command string, errMsg string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "")
		if remote.Coded != nil {
			writeRemoteStderr(stderr)
			return "", remote.Coded
		}
		if remote.Detail != "" {
			return "", operationError(fmt.Sprintf("%s: %s", errMsg, remote.Detail), "ship box doctor")
		}
		return "", operationError(errMsg, "ship box doctor")
	}
	return stdout, nil
}

func runSSHDetail(runner sshRunner, server string, command string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		remote := extractRemoteError(stdout, stderr, "remote command failed")
		if remote.Coded != nil {
			writeRemoteStderr(stderr)
			return "", remote.Coded
		}
		return "", operationError(remote.Detail, "ship box doctor")
	}
	return stdout, nil
}

func remoteCodedError(stdout, stderr string) (*errcat.Error, bool) {
	if coded, ok := errcat.ParseJSON(stdout); ok {
		return coded, true
	}
	if coded, ok := errcat.ParseJSON(stderr); ok {
		return coded, true
	}
	return nil, false
}

type remoteErrorDetail struct {
	Coded  *errcat.Error
	Detail string
}

func extractRemoteError(stdout, stderr, fallback string) remoteErrorDetail {
	if coded, ok := remoteCodedError(stdout, stderr); ok {
		return remoteErrorDetail{Coded: coded}
	}
	detail := cleanRemoteErrorText(stderr)
	if detail == "" {
		detail = cleanRemoteErrorText(stdout)
	}
	if detail == "" {
		detail = fallback
	}
	return remoteErrorDetail{Detail: detail}
}

func cleanRemoteErrorText(text string) string {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "Error: ") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "Error: "))
	}
	return text
}

func writeRemoteStderr(stderr string) {
	if strings.TrimSpace(stderr) == "" {
		return
	}
	if _, ok := errcat.ParseJSON(stderr); ok {
		return
	}
	fmt.Fprint(os.Stderr, stderr)
	if !strings.HasSuffix(stderr, "\n") {
		fmt.Fprintln(os.Stderr)
	}
}

func usageError(detail, command string) error {
	return errcat.New(errcat.CodeUsageError, errcat.Fields{
		"detail":  detail,
		"command": command,
	})
}

func operationError(detail, command string) error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  detail,
		"command": command,
	})
}

func manifestInvalidError(detail, command string) error {
	return errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
		"details": detail,
		"command": command,
	})
}

func runSSHChecked(runner sshRunner, server string, command string, errMsg string) string {
	stdout, err := runSSHRequired(runner, server, command, errMsg)
	if err != nil {
		utils.DieError(err, 1)
	}
	return stdout
}

func CmdSSHCurrent(root string) {
	server, err := BoxTarget(root)
	if err != nil {
		utils.DieError(err, 1)
	}
	runner, err := NewCommandRunner()
	if err != nil {
		utils.DieError(err, 1)
	}
	defer runner.Close()
	err = runner.RunSSHPassthrough(server, "")
	if err != nil {
		utils.DieError(err, 1)
	}
}
