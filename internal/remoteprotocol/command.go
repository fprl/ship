package remoteprotocol

import (
	"fmt"
	"regexp"
	"strings"
)

const ClientVersionFlag = "--client-version"

// ClientArgs adds the lockstep version header to a normal remote request.
// Repair and box-local commands deliberately do not use this interface.
func ClientArgs(clientVersion string, commandArgs ...string) []string {
	out := []string{ClientVersionFlag, clientVersion}
	return append(out, commandArgs...)
}

// ClientInvocation is the parsed fixed header of a normal remote request.
type ClientInvocation struct {
	ClientVersion  string
	Namespace      string
	NamespaceIndex int
}

// ParseClientArgs parses the fixed header without interpreting the
// namespace-specific tail. The client renderer, member gate, and box helper
// all share this parser.
func ParseClientArgs(args []string) (ClientInvocation, error) {
	if len(args) < 3 || args[0] != ClientVersionFlag || args[1] == "" || args[2] == "" {
		return ClientInvocation{}, fmt.Errorf("remote request requires %s <version> <namespace>", ClientVersionFlag)
	}
	return ClientInvocation{ClientVersion: args[1], Namespace: args[2], NamespaceIndex: 2}, nil
}

func ClientNamespaceAllowed(namespace string) bool {
	for _, allowed := range clientNamespaces {
		if namespace == allowed {
			return true
		}
	}
	return false
}

func RepairNamespaceAllowed(namespace string) bool {
	switch namespace {
	case "agent-shell", "update", "update-local", "version":
		return true
	default:
		return false
	}
}

var clientNamespaces = []string{"app", "approval", "config", "doctor", "gc", "key", "webhook"}

// SudoersLine is the complete remote protocol grant for the deploy user.
func SudoersLine(user string) string {
	return user + " ALL=(root) NOPASSWD: " + strings.Join(sudoersCommands(), ", ") + "\n"
}

func SudoersLineRegexp() *regexp.Regexp {
	commands := sudoersCommands()
	patterns := make([]string, 0, len(commands))
	for _, command := range commands {
		patterns = append(patterns, regexp.QuoteMeta(command))
	}
	return regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\(root\)\s+NOPASSWD:\s*` + strings.Join(patterns, `,\s*`) + `$`)
}

func sudoersCommands() []string {
	const binary = "/usr/local/bin/ship server "
	commands := make([]string, 0, len(clientNamespaces)+5)
	for _, namespace := range clientNamespaces {
		if namespace == "doctor" {
			commands = append(commands,
				binary+ClientVersionFlag+" * doctor",
				binary+ClientVersionFlag+" * doctor *",
			)
			continue
		}
		commands = append(commands, binary+ClientVersionFlag+" * "+namespace+" *")
	}
	commands = append(commands,
		binary+"version",
		binary+"version *",
		binary+"update *",
	)
	return commands
}
