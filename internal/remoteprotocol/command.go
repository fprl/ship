package remoteprotocol

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	ClientVersionFlag     = "--client-version"
	InternalFlag          = "--internal"
	MemberFingerprintFlag = "--member-fingerprint"
)

// Exposure says who may invoke a remote protocol command. A command may have
// more than one exposure (gc is both a client command and a box timer).
type Exposure uint8

const (
	ExposureClient Exposure = 1 << iota
	ExposureRepair
	ExposureInternal
	ExposureGateway
)

// Command is one closed remote protocol operation. Path contains only Kong
// command tokens; flags and positional arguments are deliberately excluded.
type Command struct {
	Path     []string
	Exposure Exposure
}

// Catalogue is the single command vocabulary shared by client rendering,
// helper admission, sudoers generation, and the forced-command SSH gate.
func Catalogue() []Command {
	out := make([]Command, len(commandCatalogue))
	for i, command := range commandCatalogue {
		out[i] = Command{Path: append([]string(nil), command.Path...), Exposure: command.Exposure}
	}
	return out
}

var commandCatalogue = []Command{
	{[]string{"app", "apply"}, ExposureClient},
	{[]string{"app", "converge"}, ExposureClient},
	{[]string{"app", "data", "fork"}, ExposureClient},
	{[]string{"app", "data", "reset"}, ExposureClient},
	{[]string{"app", "data", "restore"}, ExposureClient},
	{[]string{"app", "data", "save"}, ExposureClient},
	{[]string{"app", "destroy"}, ExposureClient},
	{[]string{"app", "destroy-env"}, ExposureClient},
	{[]string{"app", "exec"}, ExposureClient},
	{[]string{"app", "logs"}, ExposureClient},
	{[]string{"app", "ls"}, ExposureClient},
	{[]string{"app", "preflight"}, ExposureClient},
	{[]string{"app", "preview", "pin"}, ExposureClient},
	{[]string{"app", "preview", "resolve"}, ExposureClient},
	{[]string{"app", "preview", "resolve-or-create"}, ExposureClient},
	{[]string{"app", "preview", "share"}, ExposureClient},
	{[]string{"app", "preview", "unpin"}, ExposureClient},
	{[]string{"app", "rollback"}, ExposureClient},
	{[]string{"app", "secret", "list"}, ExposureClient},
	{[]string{"app", "secret", "rm"}, ExposureClient},
	{[]string{"app", "secret", "set"}, ExposureClient},
	{[]string{"app", "setup-env"}, ExposureClient},
	{[]string{"app", "status"}, ExposureClient},
	{[]string{"app", "why"}, ExposureClient},
	{[]string{"approval", "grant"}, ExposureClient},
	{[]string{"approval", "ls"}, ExposureClient},
	{[]string{"config", "get"}, ExposureClient},
	{[]string{"config", "set"}, ExposureClient},
	{[]string{"config", "unset"}, ExposureClient},
	{[]string{"doctor"}, ExposureClient},
	{[]string{"gc"}, ExposureClient | ExposureInternal},
	{[]string{"key", "add"}, ExposureClient},
	{[]string{"key", "ls"}, ExposureClient},
	{[]string{"key", "rename"}, ExposureClient},
	{[]string{"key", "rm"}, ExposureClient},
	{[]string{"key", "role"}, ExposureClient},
	{[]string{"webhook", "clear"}, ExposureClient},
	{[]string{"webhook", "get"}, ExposureClient},
	{[]string{"webhook", "set"}, ExposureClient},
	{[]string{"version"}, ExposureRepair},
	{[]string{"update"}, ExposureRepair},
	{[]string{"converge-boot"}, ExposureInternal},
	{[]string{"doctor", "record"}, ExposureInternal},
	{[]string{"env", "reap"}, ExposureInternal},
	{[]string{"agent-shell"}, ExposureGateway},
	{[]string{"update-local"}, ExposureGateway},
}

// Invocation is a classified request after its fixed protocol header has been
// removed. Args retain the complete original server argv.
type Invocation struct {
	ClientVersion  string
	Command        Command
	Exposure       Exposure
	Args           []string
	NamespaceIndex int
}

// ClientArgs adds the lockstep version header to a normal remote request.
func ClientArgs(clientVersion string, commandArgs ...string) []string {
	out := []string{ClientVersionFlag, clientVersion}
	return append(out, commandArgs...)
}

// Parse classifies one complete argv following `ship server`.
func Parse(args []string) (Invocation, error) {
	if len(args) == 0 {
		return Invocation{}, fmt.Errorf("empty remote request")
	}
	exposure := ExposureRepair | ExposureGateway
	commandArgs := args
	clientVersion := ""
	namespaceIndex := 0
	switch args[0] {
	case ClientVersionFlag:
		if len(args) < 3 || args[1] == "" {
			return Invocation{}, fmt.Errorf("remote request requires %s <version> <command>", ClientVersionFlag)
		}
		exposure = ExposureClient
		clientVersion = args[1]
		commandArgs = args[2:]
		namespaceIndex = 2
	case InternalFlag:
		if len(args) < 2 {
			return Invocation{}, fmt.Errorf("internal remote request requires a command")
		}
		exposure = ExposureInternal
		commandArgs = args[1:]
		namespaceIndex = 1
	}
	command, ok := lookupCommand(withoutMemberClaims(commandArgs), exposure)
	if !ok {
		return Invocation{}, fmt.Errorf("remote command is not available for this invocation")
	}
	return Invocation{
		ClientVersion:  clientVersion,
		Command:        command,
		Exposure:       command.Exposure & exposure,
		Args:           append([]string(nil), args...),
		NamespaceIndex: namespaceIndex,
	}, nil
}

func withoutMemberClaims(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		if args[i] == MemberFingerprintFlag {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(args[i], MemberFingerprintFlag+"=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func lookupCommand(args []string, exposure Exposure) (Command, bool) {
	var best Command
	for _, command := range commandCatalogue {
		if command.Exposure&exposure == 0 || len(command.Path) > len(args) {
			continue
		}
		matched := true
		for i := range command.Path {
			if args[i] != command.Path[i] {
				matched = false
				break
			}
		}
		if matched && len(command.Path) > len(best.Path) {
			best = command
		}
	}
	if len(best.Path) == 0 {
		return Command{}, false
	}
	best.Path = append([]string(nil), best.Path...)
	return best, true
}

// PathAllowed validates a Kong-resolved command path against the catalogue.
func PathAllowed(path []string, exposure Exposure) bool {
	command, ok := lookupCommand(path, exposure)
	return ok && len(command.Path) == len(path)
}

// CommandAllowed accepts a resolved command followed by arguments/placeholders.
// It is used by parser adapters such as Kong after they resolve the command.
func CommandAllowed(args []string, exposure Exposure) bool {
	_, ok := lookupCommand(args, exposure)
	return ok
}

// ClientInvocation is retained as the narrow fixed-header view used by a few
// callers. New policy should use Parse, which validates the complete path.
type ClientInvocation struct {
	ClientVersion  string
	Namespace      string
	NamespaceIndex int
}

func ParseClientArgs(args []string) (ClientInvocation, error) {
	invocation, err := Parse(args)
	if err != nil || invocation.Exposure != ExposureClient {
		if err == nil {
			err = fmt.Errorf("remote request is not client-exposed")
		}
		return ClientInvocation{}, err
	}
	return ClientInvocation{
		ClientVersion:  invocation.ClientVersion,
		Namespace:      invocation.Command.Path[0],
		NamespaceIndex: invocation.NamespaceIndex,
	}, nil
}

func ClientNamespaceAllowed(namespace string) bool {
	return namespaceAllowed(namespace, ExposureClient)
}

func RepairNamespaceAllowed(namespace string) bool {
	return namespaceAllowed(namespace, ExposureRepair|ExposureGateway)
}

func namespaceAllowed(namespace string, exposure Exposure) bool {
	for _, command := range commandCatalogue {
		if command.Exposure&exposure != 0 && command.Path[0] == namespace {
			return true
		}
	}
	return false
}

// BindMember replaces every client-supplied member claim with the pinned SSH
// key fingerprint. Identity is inserted at the Kong namespace that owns the
// flag; client input can never win by ordering or --flag=value spelling.
func BindMember(invocation Invocation, fingerprint string) (Invocation, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return Invocation{}, fmt.Errorf("missing pinned member fingerprint")
	}
	if invocation.Exposure != ExposureClient && invocation.Exposure != ExposureRepair {
		return Invocation{}, fmt.Errorf("member identity cannot be bound to this invocation")
	}
	index := invocation.NamespaceIndex
	if index < 0 || index >= len(invocation.Args) {
		return Invocation{}, fmt.Errorf("invalid remote invocation namespace")
	}
	out := append([]string(nil), invocation.Args[:index+1]...)
	out = append(out, MemberFingerprintFlag, fingerprint)
	tail := invocation.Args[index+1:]
	for i := 0; i < len(tail); i++ {
		if tail[i] == "--" {
			out = append(out, tail[i:]...)
			break
		}
		if tail[i] == MemberFingerprintFlag {
			if i+1 < len(tail) {
				i++
			}
			continue
		}
		if strings.HasPrefix(tail[i], MemberFingerprintFlag+"=") {
			continue
		}
		out = append(out, tail[i])
	}
	invocation.Args = out
	return invocation, nil
}

// ParseShellFields parses the quoting emitted by RenderShellFields. It does
// not execute expansions. An unquoted shell control operator is emitted as its
// own token so the agent-shell gate can reject it even when it is written
// attached to another word ("ls;rm"); the same operator inside quotes is
// preserved as literal argument text.
func ParseShellFields(value string) ([]string, error) {
	var fields []string
	var b strings.Builder
	inSingle, inDouble, escaped, started := false, false, false, false
	flush := func() {
		if started {
			fields = append(fields, b.String())
			b.Reset()
			started = false
		}
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			started = true
			continue
		}
		if inSingle {
			started = true
			if c == '\'' {
				inSingle = false
			} else {
				b.WriteByte(c)
			}
			continue
		}
		if inDouble {
			started = true
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
		case '\'', '"':
			started = true
			inSingle = c == '\''
			inDouble = c == '"'
		case '\\':
			escaped = true
			started = true
		case ' ', '\t':
			flush()
		case '\r', '\n':
			return nil, fmt.Errorf("command contains a newline")
		case ';', '|', '<', '>':
			flush()
			fields = append(fields, string(c))
		case '&':
			flush()
			if i+1 < len(value) && value[i+1] == '&' {
				fields = append(fields, "&&")
				i++
			} else {
				fields = append(fields, "&")
			}
		default:
			started = true
			b.WriteByte(c)
		}
	}
	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unterminated shell quoting")
	}
	flush()
	return fields, nil
}

func RenderShellFields(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if arg == "" {
			parts[i] = "''"
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(parts, " ")
}

// SudoersLine is the complete client/repair protocol grant for the deploy user.
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
	set := map[string]bool{}
	for _, command := range commandCatalogue {
		if command.Exposure&ExposureClient != 0 {
			set[command.Path[0]] = true
		}
	}
	namespaces := make([]string, 0, len(set))
	for namespace := range set {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	commands := make([]string, 0, len(namespaces)+4)
	for _, namespace := range namespaces {
		if namespace == "doctor" {
			commands = append(commands, binary+ClientVersionFlag+" * doctor", binary+ClientVersionFlag+" * doctor *")
			continue
		}
		commands = append(commands, binary+ClientVersionFlag+" * "+namespace+" *")
	}
	commands = append(commands, binary+"version", binary+"version *", binary+"update *")
	return commands
}
