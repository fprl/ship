package client

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/knownhosts"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/remoteprotocol"
	"github.com/fprl/ship/internal/shipidentity"
	"github.com/fprl/ship/internal/utils"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CommandRunner struct {
	SshOptions        []string
	RsyncRemoteShell  string
	TempDir           string
	MemberFingerprint string
}

func NewCommandRunner() (*CommandRunner, error) {
	sshOpts := []string{"-o", "BatchMode=yes"}
	knownHostOpts, err := knownhosts.CanonicalSSHOptions("accept-new")
	if err != nil {
		return nil, err
	}
	sshOpts = append(sshOpts, knownHostOpts...)
	identity, err := shipidentity.EnsureShipIdentity(shipidentity.Options{Output: os.Stderr})
	if err != nil {
		return nil, err
	}
	key := os.Getenv("SHIP_SSH_KEY")
	if key == "" {
		fingerprint, err := fingerprintForPublicKeyLine(identity.PublicKeyLine)
		if err != nil {
			return nil, err
		}
		sshOpts = append(sshOpts,
			"-i", identity.PrivateKeyPath,
			"-o", "IdentitiesOnly=yes",
		)
		return &CommandRunner{
			SshOptions:        sshOpts,
			RsyncRemoteShell:  sshRemoteShell(sshOpts),
			MemberFingerprint: fingerprint,
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
	publicLine, err := publicKeyLineForPrivateKey(keyPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if comment := privateKeyComment(keyPath); comment != "" && len(strings.Fields(publicLine)) < 3 {
		publicLine = strings.TrimSpace(publicLine) + " " + comment
	}
	if err := os.WriteFile(keyPath+".pub", []byte(strings.TrimSpace(publicLine)+"\n"), 0644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	fingerprint, err := fingerprintForPublicKeyLine(publicLine)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	sshOpts = append(sshOpts,
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
	)

	return &CommandRunner{
		SshOptions:        sshOpts,
		RsyncRemoteShell:  sshRemoteShell(sshOpts),
		TempDir:           dir,
		MemberFingerprint: fingerprint,
	}, nil
}

func publicKeyLineForPrivateKey(path string) (string, error) {
	stdout, stderr, code, err := runCommand("ssh-keygen", []string{"-y", "-f", path}, "")
	if err != nil || code != 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = "ssh-keygen -y failed"
		}
		return "", errcat.New(errcat.CodeOperationFailed, errcat.Fields{
			"detail":  "ship identity setup failed: " + detail,
			"command": "fix SHIP_SSH_KEY",
		})
	}
	return strings.TrimSpace(stdout), nil
}

func privateKeyComment(path string) string {
	stdout, _, code, _ := runCommand("ssh-keygen", []string{"-l", "-f", path}, "")
	if code != 0 {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) < 3 {
		return ""
	}
	end := len(fields)
	if strings.HasPrefix(fields[end-1], "(") {
		end--
	}
	if end <= 2 {
		return ""
	}
	return strings.Join(fields[2:end], " ")
}

func fingerprintForPublicKeyLine(line string) (string, error) {
	key, err := memberkeys.NormalizeLine(line, "")
	if err != nil {
		return "", err
	}
	return key.Fingerprint, nil
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

func deploySSHTarget(server string) string {
	server = strings.TrimSpace(server)
	if server == "" || strings.Contains(server, "@") {
		return server
	}
	return DefaultDeployUser + "@" + server
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
	args = append(args, "-G", deploySSHTarget(server))
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
	command = r.withMemberFingerprint(command)
	args = append(args, deploySSHTarget(server), command)
	stdout, stderr, code, err := runCommand("ssh", args, "")
	return stdout, stderr, code, mapSSHTransportError(server, stderr, code, err)
}

// RunSSHStreaming preserves stdout for the helper's success/error contract
// while delivering complete stderr lines as they arrive. Lines consumed by
// onStderrLine are omitted from returned stderr; ordinary warnings and SSH
// diagnostics remain available to the normal error mapper.
func (r *CommandRunner) RunSSHStreaming(server string, command string, onStderrLine func(string) bool) (string, string, int, error) {
	return r.runSSHStreaming(server, command, nil, onStderrLine)
}

// RunSSHStreamingFile streams path as the remote command's stdin while
// preserving the helper progress and error contracts.
func (r *CommandRunner) RunSSHStreamingFile(server string, command string, path string, onStderrLine func(string) bool) (string, string, int, error) {
	input, err := os.Open(path)
	if err != nil {
		return "", "", 1, err
	}
	defer input.Close()
	return r.runSSHStreaming(server, command, input, onStderrLine)
}

func (r *CommandRunner) runSSHStreaming(server string, command string, input io.Reader, onStderrLine func(string) bool) (string, string, int, error) {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	command = r.withMemberFingerprint(command)
	args = append(args, deploySSHTarget(server), command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", "", 1, err
	}
	if err := cmd.Start(); err != nil {
		return "", "", 1, mapSSHTransportError(server, "", 1, err)
	}
	scanner := bufio.NewScanner(stderrPipe)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if onStderrLine != nil && onStderrLine(line) {
			continue
		}
		stderr.WriteString(line)
		stderr.WriteByte('\n')
	}
	scanErr := scanner.Err()
	runErr := cmd.Wait()
	if scanErr != nil && runErr == nil {
		runErr = scanErr
	}
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	stderrText := stderr.String()
	return stdout.String(), stderrText, code, mapSSHTransportError(server, stderrText, code, runErr)
}

// RunSSHToFile streams remote stdout directly to path. On failure it returns
// the small stdout error payload so callers can preserve errcat errors before
// removing the incomplete archive.
func (r *CommandRunner) RunSSHToFile(server, command, path string) (string, string, int, error) {
	var args []string
	args = append(args, r.SshOptions...)
	command = r.withMemberFingerprint(command)
	args = append(args, deploySSHTarget(server), command)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", "", 1, err
	}
	cmd := exec.Command("ssh", args...)
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := out.Close()
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	if closeErr != nil && runErr == nil {
		runErr, code = closeErr, 1
	}
	stderrText := stderr.String()
	if runErr == nil && code == 0 {
		return "", stderrText, 0, nil
	}
	failure, _ := os.ReadFile(path)
	if len(failure) > 64*1024 {
		failure = failure[len(failure)-64*1024:]
	}
	return string(failure), stderrText, code, mapSSHTransportError(server, stderrText, code, runErr)
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
	command = r.withMemberFingerprint(command)
	args = append(args, deploySSHTarget(server), command)
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
	out := stdout.String()
	errOut := stderr.String()
	return out, errOut, code, mapSSHTransportError(server, errOut, code, err)
}

func (r *CommandRunner) RunSSHPassthrough(server string, command string) error {
	if err := r.preflightHostKey(server); err != nil {
		return err
	}
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	if command != "" {
		command = r.withMemberFingerprint(command)
		args = append(args, deploySSHTarget(server), command)
	} else {
		args = append(args, deploySSHTarget(server))
	}
	return runCommandPassthrough("ssh", args)
}

func (r *CommandRunner) RunSSHPassthroughExitCode(server string, command string, tty bool) (int, error) {
	if err := r.preflightHostKey(server); err != nil {
		return 1, err
	}
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	if tty {
		args = append(args, "-tt")
	}
	command = r.withMemberFingerprint(command)
	args = append(args, deploySSHTarget(server), command)
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

func (r *CommandRunner) preflightHostKey(server string) error {
	var args []string
	if len(r.SshOptions) > 0 {
		args = append(args, r.SshOptions...)
	}
	args = append(args, deploySSHTarget(server), "true")
	_, stderr, code, err := runCommand("ssh", args, "")
	mapped := mapSSHTransportError(server, stderr, code, err)
	if coded, ok := errcat.As(mapped); ok && coded.Code() == errcat.CodeHostKeyChanged {
		return coded
	}
	return nil
}

const remoteServerCommandPrefix = "sudo -n /usr/local/bin/ship server "

func (r *CommandRunner) withMemberFingerprint(command string) string {
	fingerprint := strings.TrimSpace(r.MemberFingerprint)
	if fingerprint == "" {
		return command
	}
	index := strings.Index(command, remoteServerCommandPrefix)
	if index < 0 {
		return command
	}
	prefix := command[:index]
	argv, err := remoteprotocol.ParseShellFields(command[index:])
	if err != nil || len(argv) < 5 || argv[0] != "sudo" || argv[1] != "-n" || argv[2] != "/usr/local/bin/ship" || argv[3] != "server" {
		return command
	}
	invocation, err := remoteprotocol.Parse(argv[4:])
	if err != nil {
		return command
	}
	bound, err := remoteprotocol.BindMember(invocation, fingerprint)
	if err != nil {
		return command
	}
	return prefix + renderServerCommand(bound.Args...)
}

func (r *CommandRunner) Upload(local string, remote string, server string) error {
	var args []string
	if r.RsyncRemoteShell != "" {
		args = append(args, "-e", r.RsyncRemoteShell)
	}
	args = append(args, "-az", local, fmt.Sprintf("%s:%s", deploySSHTarget(server), remote))
	_, stderr, code, err := runCommand("rsync", args, "")
	if err != nil || code != 0 {
		if sshHostKeyFailure(stderr) {
			return hostKeyChangedError(server)
		}
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

func runSSHRequired(runner sshRunner, server string, command string, errMsg string, remediation string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		if err := sshResultError(server, stdout, stderr, code, err, errMsg, errMsg, remediation); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return stdout, nil
}

func runSSHDetail(runner sshRunner, server string, command string, remediation string) (string, error) {
	stdout, stderr, code, err := runner.RunSSH(server, command)
	if err != nil || code != 0 {
		if err := sshResultError(server, stdout, stderr, code, err, "", "remote command failed", remediation); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(stderr) != "" {
		fmt.Fprint(os.Stderr, stderr)
	}
	return stdout, nil
}

func sshResultError(server, stdout, stderr string, code int, err error, prefix string, fallback string, remediation string) error {
	outcome := decodeRemoteOutcome(stdout, stderr, code, err, "", server)
	if outcome.TransportCoded != nil {
		return outcome.TransportCoded
	}
	if outcome.RemoteCoded != nil {
		writeRemoteStderr(outcome)
		return outcome.RemoteCoded
	}
	detail := outcome.Detail
	if detail != "" && prefix != "" {
		detail = fmt.Sprintf("%s: %s", prefix, detail)
	}
	if detail == "" {
		detail = fallback
	}
	return operationError(detail, remediation)
}

type remoteOutcome struct {
	TransportCoded *errcat.Error
	RemoteCoded    *errcat.Error
	Detail         string
	Stderr         string
	ForwardStderr  bool
}

func decodeRemoteOutcome(stdout, stderr string, code int, err error, fallback string, target ...string) remoteOutcome {
	outcome := remoteOutcome{Stderr: stderr}
	if coded, ok := errcat.As(err); ok {
		outcome.TransportCoded = coded
		return outcome
	}
	if coded, ok := errcat.ParseJSON(stdout); ok {
		outcome.RemoteCoded = remoteErrorForTarget(coded, target...)
		if strings.TrimSpace(stderr) != "" {
			if _, stderrIsErrorJSON := errcat.ParseJSON(stderr); !stderrIsErrorJSON {
				outcome.ForwardStderr = true
			}
		}
		return outcome
	}
	if coded, ok := errcat.ParseJSON(stderr); ok {
		outcome.RemoteCoded = remoteErrorForTarget(coded, target...)
		return outcome
	}
	outcome.Detail = cleanRemoteErrorText(stderr)
	if outcome.Detail == "" {
		outcome.Detail = cleanRemoteErrorText(stdout)
	}
	if outcome.Detail == "" {
		outcome.Detail = fallback
	}
	return outcome
}

func remoteErrorForTarget(coded *errcat.Error, target ...string) *errcat.Error {
	if len(target) == 0 || strings.TrimSpace(target[0]) == "" || !strings.Contains(coded.Remediation(), "<box>") {
		return coded
	}
	bound, _ := errcat.As(errcat.WithRemediation(coded, strings.ReplaceAll(coded.Remediation(), "<box>", target[0])))
	return bound
}

func cleanRemoteErrorText(text string) string {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "Error: ") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "Error: "))
	}
	return text
}

func writeRemoteStderr(outcome remoteOutcome) {
	if !outcome.ForwardStderr {
		return
	}
	stderr := outcome.Stderr
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

func mapSSHTransportError(server, stderr string, code int, err error) error {
	if (err != nil || code != 0) && sshHostKeyFailure(stderr) {
		return hostKeyChangedError(server)
	}
	return err
}

func hostKeyChangedError(server string) error {
	box := strings.TrimSpace(server)
	if host, ok := config.UserHostBoxHost(box); ok {
		box = host
	}
	return errcat.New(errcat.CodeHostKeyChanged, errcat.Fields{"box": box})
}

func sshHostKeyFailure(stderr string) bool {
	text := strings.ToLower(stderr)
	for _, pattern := range []string{
		"remote host identification has changed",
		"host key verification failed",
		"no ed25519 host key is known",
		"no ecdsa host key is known",
		"no rsa host key is known",
		"offending key",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func manifestInvalidError(detail, command string) error {
	return errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
		"details": detail,
		"command": command,
	})
}

func runSSHChecked(runner sshRunner, server string, command string, errMsg string, remediation string) string {
	stdout, err := runSSHRequired(runner, server, command, errMsg, remediation)
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
