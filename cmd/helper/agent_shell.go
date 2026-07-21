package helper

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/remoteprotocol"
	"github.com/fprl/ship/kernel"
)

type agentShellCmd struct {
	MemberFingerprint string `name:"member-fingerprint" required:"" help:"Pinned member SSH public key fingerprint from authorized_keys."`
}

type agentShellAction struct {
	Kind string
	Args []string
	Path string
}

const (
	agentShellActionExec          = "exec"
	agentShellActionPrepareUpload = "prepare-upload"
	agentShellActionCleanupUpload = "cleanup-upload"
)

func (c agentShellCmd) Run() error {
	action, err := agentShellActionFor(os.Getenv("SSH_ORIGINAL_COMMAND"), c.MemberFingerprint)
	if err != nil {
		utilsDieError(err)
	}
	switch action.Kind {
	case agentShellActionPrepareUpload:
		if err := os.MkdirAll(action.Path, 0700); err != nil {
			return fmt.Errorf("prepare upload dir: %v", err)
		}
		return os.Chmod(action.Path, 0700)
	case agentShellActionCleanupUpload:
		return os.RemoveAll(action.Path)
	case agentShellActionExec:
		path, err := exec.LookPath(action.Args[0])
		if err != nil {
			return err
		}
		return syscall.Exec(path, action.Args, os.Environ())
	default:
		return agentShellRefused("unsupported action")
	}
}

func agentShellActionFor(original, fingerprint string) (agentShellAction, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return agentShellAction{}, agentShellRefused("missing pinned member fingerprint")
	}
	original = strings.TrimSpace(original)
	if original == "" {
		return agentShellAction{}, agentShellRefused("interactive sessions are disabled")
	}
	if strings.ContainsAny(original, "\r\n") || strings.Contains(original, "$(") || strings.Contains(original, "`") {
		return agentShellAction{}, agentShellRefused("command contains shell injection syntax")
	}
	argv, err := remoteprotocol.ParseShellFields(original)
	if err != nil {
		return agentShellAction{}, agentShellRefused(err.Error())
	}
	switch {
	case isAgentHelperCommand(argv):
		return agentShellAction{Kind: agentShellActionExec, Args: forceAgentMemberFingerprint(argv, fingerprint)}, nil
	case isPrepareUploadCommand(argv):
		return agentShellAction{Kind: agentShellActionPrepareUpload, Path: argv[2]}, nil
	case isCleanupUploadCommand(argv):
		return agentShellAction{Kind: agentShellActionCleanupUpload, Path: argv[2]}, nil
	case isRsyncUploadCommand(argv):
		return agentShellAction{Kind: agentShellActionExec, Args: argv}, nil
	default:
		return agentShellAction{}, agentShellRefused("command is outside the agent allowlist")
	}
}

func isAgentHelperCommand(argv []string) bool {
	_, ok := agentHelperInvocation(argv)
	return ok && !hasShellControlToken(argv)
}

func agentHelperInvocation(argv []string) (remoteprotocol.Invocation, bool) {
	if len(argv) < 5 || argv[0] != "sudo" || argv[1] != "-n" || argv[2] != "/usr/local/bin/ship" || argv[3] != "server" {
		return remoteprotocol.Invocation{}, false
	}
	invocation, err := remoteprotocol.Parse(argv[4:])
	if err != nil {
		return remoteprotocol.Invocation{}, false
	}
	if invocation.Exposure != kernel.ExposureClient && invocation.Exposure != kernel.ExposureRepair {
		return remoteprotocol.Invocation{}, false
	}
	return invocation, true
}

// hasShellControlToken rejects an unquoted shell control operator standing as
// its own token. ParseShellFields splits unquoted control punctuation into
// standalone tokens, so an attached form like "ls;rm" surfaces here as a bare
// ";" while a quoted argument such as "Smoke <x@y>" stays one literal token.
func hasShellControlToken(argv []string) bool {
	for _, arg := range argv {
		switch arg {
		case ";", "|", "||", "&&", "&", "<", ">":
			return true
		}
	}
	return false
}

func forceAgentMemberFingerprint(argv []string, fingerprint string) []string {
	invocation, ok := agentHelperInvocation(argv)
	if !ok {
		return append([]string{}, argv...)
	}
	bound, err := remoteprotocol.BindMember(invocation, fingerprint)
	if err != nil {
		return append([]string{}, argv...)
	}
	return append(append([]string{}, argv[:4]...), bound.Args...)
}

func isPrepareUploadCommand(argv []string) bool {
	return len(argv) == 7 &&
		argv[0] == "mkdir" &&
		argv[1] == "-p" &&
		argv[3] == "&&" &&
		argv[4] == "chmod" &&
		argv[5] == "0700" &&
		argv[2] == argv[6] &&
		validShipUploadPath(argv[2], true)
}

func isCleanupUploadCommand(argv []string) bool {
	return len(argv) == 3 &&
		argv[0] == "rm" &&
		argv[1] == "-rf" &&
		validShipUploadPath(argv[2], true)
}

func isRsyncUploadCommand(argv []string) bool {
	if len(argv) < 5 || argv[0] != "rsync" || argv[1] != "--server" {
		return false
	}
	if argv[len(argv)-2] != "." {
		return false
	}
	return validShipUploadPath(argv[len(argv)-1], false)
}

func validShipUploadPath(path string, dirOnly bool) bool {
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	clean := filepath.Clean(path)
	if clean != path || !strings.HasPrefix(clean, "/tmp/ship-deploy/") {
		return false
	}
	rel := strings.TrimPrefix(clean, "/tmp/ship-deploy/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return false
	}
	parts := strings.Split(rel, "/")
	if !strings.HasPrefix(parts[0], "data-restore-") || len(parts[0]) == len("data-restore-") {
		return false
	}
	if dirOnly {
		return len(parts) == 1
	}
	return len(parts) == 2 && parts[1] == "snapshot.data.tar.gz"
}

func agentShellRefused(detail string) error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "agent_shell_refused: " + detail,
		"command": "ship",
	})
}
