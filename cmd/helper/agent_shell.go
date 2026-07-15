package helper

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fprl/ship/internal/errcat"
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
	argv, err := shellFields(original)
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
	return len(argv) >= 5 &&
		argv[0] == "sudo" &&
		argv[1] == "-n" &&
		argv[2] == "/usr/local/bin/ship" &&
		argv[3] == "server" &&
		agentHelperNamespaceAllowed(argv[4]) &&
		!hasShellControlToken(argv)
}

func agentHelperNamespaceAllowed(namespace string) bool {
	switch namespace {
	case "app", "approval", "config", "doctor", "key", "webhook", "version", "update":
		return true
	default:
		return false
	}
}

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
	out := append([]string{}, argv[:5]...)
	out = append(out, "--member-fingerprint", fingerprint)
	tail := argv[5:]
	for i := 0; i < len(tail); i++ {
		if tail[i] == "--" {
			out = append(out, tail[i:]...)
			break
		}
		// Drop every client-supplied member/fingerprint claim in any
		// spelling. The pinned fingerprint injected above is the only
		// identity the helper may see; leaving a `--flag=value` form in
		// the tail lets Kong's last-value-wins override it and lets an
		// agent key authorize as an arbitrary member.
		if flag, inlineValue := isForcedMemberClaim(tail[i]); flag {
			if !inlineValue {
				i++ // also drop the following value token
			}
			continue
		}
		out = append(out, tail[i])
	}
	return out
}

func isForcedMemberClaim(token string) (isFlag bool, hasInlineValue bool) {
	switch {
	case token == "--member-fingerprint":
		return true, false
	case strings.HasPrefix(token, "--member-fingerprint="):
		return true, true
	}
	return false, false
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
	if dirOnly {
		return !strings.Contains(filepath.Base(rel), ".tar") && filepath.Base(rel) != "ship.toml"
	}
	return true
}

func shellFields(s string) ([]string, error) {
	var fields []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			fields = append(fields, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				b.WriteByte(c)
			}
			continue
		}
		if inDouble {
			switch c {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				b.WriteByte(c)
			}
			continue
		}
		switch c {
		case ' ', '\t':
			flush()
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '\\':
			escaped = true
		case ';', '|', '<', '>':
			flush()
			fields = append(fields, string(c))
		case '&':
			flush()
			if i+1 < len(s) && s[i+1] == '&' {
				fields = append(fields, "&&")
				i++
			} else {
				fields = append(fields, "&")
			}
		default:
			b.WriteByte(c)
		}
	}
	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unterminated shell quoting")
	}
	flush()
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return fields, nil
}

func agentShellRefused(detail string) error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "agent_shell_refused: " + detail,
		"command": "ship",
	})
}
