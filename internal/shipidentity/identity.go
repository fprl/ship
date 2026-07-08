package shipidentity

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/errcat"
)

const (
	privateKeyName = "ship"
	publicKeyName  = "ship.pub"
)

type Options struct {
	HomeDir       string
	Env           map[string]string
	Output        io.Writer
	GitUserName   func() string
	CommandRunner func(name string, args ...string) error
}

type Identity struct {
	Name           string
	PrivateKeyPath string
	PublicKeyPath  string
	PublicKeyLine  string
	Created        bool
}

func EnsureShipIdentity(opts Options) (Identity, error) {
	home, err := homeDir(opts.HomeDir)
	if err != nil {
		return Identity{}, identityError("resolve home directory: " + err.Error())
	}
	sshDir := filepath.Join(home, ".ssh")
	privateKey := filepath.Join(sshDir, privateKeyName)
	publicKey := filepath.Join(sshDir, publicKeyName)

	created := false
	if _, err := os.Stat(privateKey); err != nil {
		if !os.IsNotExist(err) {
			return Identity{}, identityError("stat ~/.ssh/ship: " + err.Error())
		}
		if err := ensureSSHDir(sshDir); err != nil {
			return Identity{}, err
		}
		name := DeriveMemberName(gitUserName(opts), envValue(opts.Env, "USER"))
		if err := runKeygen(opts, privateKey, name); err != nil {
			return Identity{}, identityError("create ~/.ssh/ship: " + err.Error())
		}
		if err := os.Chmod(privateKey, 0600); err != nil {
			return Identity{}, identityError("chmod ~/.ssh/ship: " + err.Error())
		}
		if err := os.Chmod(publicKey, 0644); err != nil {
			return Identity{}, identityError("chmod ~/.ssh/ship.pub: " + err.Error())
		}
		created = true
	}

	identity, err := ReadShipIdentity(opts.HomeDir)
	if err != nil {
		return Identity{}, err
	}
	identity.Created = created
	if created && opts.Output != nil {
		fmt.Fprintf(opts.Output, "identity: %s (created ~/.ssh/ship)\n", identity.Name)
	}
	return identity, nil
}

func ReadShipIdentity(homeOverride string) (Identity, error) {
	home, err := homeDir(homeOverride)
	if err != nil {
		return Identity{}, identityError("resolve home directory: " + err.Error())
	}
	privateKey := filepath.Join(home, ".ssh", privateKeyName)
	publicKey := filepath.Join(home, ".ssh", publicKeyName)
	data, err := os.ReadFile(publicKey)
	if err != nil {
		return Identity{}, identityError("read ~/.ssh/ship.pub: " + err.Error())
	}
	line := strings.TrimSpace(string(data))
	name := PublicKeyComment(line)
	if name == "" {
		return Identity{}, identityError("read ~/.ssh/ship.pub: public key comment is empty")
	}
	return Identity{
		Name:           name,
		PrivateKeyPath: privateKey,
		PublicKeyPath:  publicKey,
		PublicKeyLine:  line,
	}, nil
}

func ShipPrivateKeyPath(homeOverride string) (string, error) {
	home, err := homeDir(homeOverride)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", privateKeyName), nil
}

func ShipPublicKeyPath(homeOverride string) (string, error) {
	home, err := homeDir(homeOverride)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", publicKeyName), nil
}

func DeriveMemberName(gitName, user string) string {
	if name := sanitizeMemberName(gitName); name != "" {
		return name
	}
	if name := sanitizeMemberName(user); name != "" {
		return name
	}
	return "ship-member"
}

func PublicKeyComment(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return ""
	}
	prefix := fields[0] + " " + fields[1]
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

func homeDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return os.UserHomeDir()
}

func ensureSSHDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return identityError("create ~/.ssh: " + err.Error())
	}
	if err := os.Chmod(path, 0700); err != nil {
		return identityError("chmod ~/.ssh: " + err.Error())
	}
	return nil
}

func gitUserName(opts Options) string {
	if opts.GitUserName != nil {
		return opts.GitUserName()
	}
	out, err := exec.Command("git", "config", "user.name").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runKeygen(opts Options, privateKey string, name string) error {
	args := []string{"-q", "-t", "ed25519", "-N", "", "-C", name, "-f", privateKey}
	if opts.CommandRunner != nil {
		return opts.CommandRunner("ssh-keygen", args...)
	}
	cmd := exec.Command("ssh-keygen", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s", detail)
	}
	return nil
}

func sanitizeMemberName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if valid {
			if r == '-' {
				if prevDash {
					continue
				}
				prevDash = true
			} else {
				prevDash = false
			}
			b.WriteRune(r)
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 39 {
		out = strings.Trim(out[:39], "-")
	}
	return out
}

func envValue(env map[string]string, name string) string {
	if env != nil {
		return env[name]
	}
	return os.Getenv(name)
}

func identityError(detail string) error {
	return errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "ship identity setup failed: " + detail,
		"command": "mkdir -p ~/.ssh && chmod 700 ~/.ssh",
	})
}
