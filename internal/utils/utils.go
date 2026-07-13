package utils

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/fprl/ship/internal/config"
	"github.com/fprl/ship/internal/errcat"
)

var shellEscapeRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func ShellEscape(value string) string {
	if shellEscapeRe.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func SystemctlBin() string {
	if b := os.Getenv("SHIP_SYSTEMCTL_BIN"); b != "" {
		return b
	}
	return "systemctl"
}

type CommandError struct {
	Name     string
	Args     []string
	Stdout   string
	Stderr   string
	Err      error
	TimedOut bool
	Timeout  time.Duration
}

func (e *CommandError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("command timed out after %s: %s %v", e.Timeout, e.Name, e.Args)
	}
	return fmt.Sprintf("command failed: %s %v: %v", e.Name, e.Args, e.Err)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

func (e *CommandError) CombinedOutput() string {
	switch {
	case e.Stderr != "" && e.Stdout != "":
		return e.Stderr + "\n" + e.Stdout
	case e.Stderr != "":
		return e.Stderr
	default:
		return e.Stdout
	}
}

var errorJSON bool

func SetErrorJSON(enabled bool) bool {
	previous := errorJSON
	errorJSON = enabled
	return previous
}

func Die(message string, code int) {
	DieError(errors.New(message), code)
}

func DieError(err error, code int) {
	coded := normalizeExitError(err, code)
	code = codedExitCode(coded.Code())
	if errorJSON {
		fmt.Println(coded.JSONLine())
		os.Exit(code)
	}
	fmt.Fprintln(os.Stderr, coded.Human())
	os.Exit(code)
}

func normalizeExitError(err error, code int) *errcat.Error {
	if coded, ok := errcat.As(err); ok {
		return coded
	}
	if details, ok := config.ManifestErrorDetails(err); ok {
		if dockerfileMissingDetails(details) {
			return errcat.New(errcat.CodeDockerfileMissing, nil)
		}
		return errcat.New(errcat.CodeManifestInvalid, errcat.Fields{
			"details": manifestDetailsCause(details),
			"command": manifestNextCommand(details),
		})
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "no error detail"
	}
	if code == 2 {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  message,
			"command": "ship help",
		})
	}
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  message,
		"command": "ship status",
	})
}

func manifestDetailsCause(details []string) string {
	if len(details) == 0 {
		return "no manifest detail"
	}
	if len(details) == 1 {
		return details[0]
	}
	lines := []string{fmt.Sprintf("manifest has %d validation errors:", len(details))}
	for _, detail := range details {
		lines = append(lines, "  - "+detail)
	}
	return strings.Join(lines, "\n")
}

func manifestNextCommand(details []string) string {
	for _, detail := range details {
		if manifestMissing(detail) {
			return "ship init"
		}
		if strings.Contains(detail, "box must be a host, not user@host") {
			return "remove the user part from ship.toml box"
		}
	}
	return "fix ship.toml"
}

func dockerfileMissingDetails(details []string) bool {
	for _, detail := range details {
		if dockerfileMissingMessage(detail) {
			return true
		}
	}
	return false
}

func dockerfileMissingMessage(message string) bool {
	return strings.Contains(message, "missing a Dockerfile")
}

func manifestMissing(message string) bool {
	return strings.Contains(message, "ship.toml not found") || strings.Contains(message, "ship.toml was not found")
}

func codedExitCode(code errcat.Code) int {
	switch code {
	case errcat.CodeUsageError,
		errcat.CodeManifestInvalid,
		errcat.CodeDockerfileMissing,
		errcat.CodeMultiProcessNoWebRoute,
		errcat.CodeInvalidSecretKey,
		errcat.CodeLogsFollowJSONConflict,
		errcat.CodeDotenvMalformed,
		errcat.CodeBoxTargetRequired,
		errcat.CodeInvalidBoxTarget:
		return 2
	default:
		return 1
	}
}

func ExitCodeForErrorCode(code errcat.Code) int {
	return codedExitCode(code)
}

func RunChecked(name string, args []string, cwd string) ([]byte, error) {
	return runChecked(nil, 0, name, args, cwd)
}

func RunCheckedWithTimeout(name string, args []string, cwd string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runChecked(ctx, timeout, name, args, cwd)
}

func runChecked(ctx context.Context, timeout time.Duration, name string, args []string, cwd string) ([]byte, error) {
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			os.Stderr.Write(stderr.Bytes())
		}
		if stdout.Len() > 0 {
			os.Stderr.Write(stdout.Bytes())
		}
		cmdErr := &CommandError{
			Name:     name,
			Args:     append([]string(nil), args...),
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Err:      err,
			TimedOut: ctx != nil && ctx.Err() == context.DeadlineExceeded,
			Timeout:  timeout,
		}
		return nil, cmdErr
	}
	return stdout.Bytes(), nil
}
