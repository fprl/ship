package helper

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

type keyCmd struct {
	Add keyAddCmd `cmd:"add" help:"Append SSH public keys to the deploy user's authorized_keys."`
}

type keyAddCmd struct {
	Comment string `name:"comment" required:"" help:"Comment to stamp on appended keys."`
}

type authorizedKey struct {
	Line     string
	Material string
}

func (c keyAddCmd) Run() error {
	if err := c.run(); err != nil {
		utils.DieError(err, 1)
	}
	return nil
}

func (c keyAddCmd) run() error {
	comment := strings.Join(strings.Fields(c.Comment), " ")
	if comment == "" {
		return fmt.Errorf("key comment is required")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("read public keys from stdin: %v", err)
	}
	keys, err := normalizeAuthorizedKeys(string(raw), comment)
	if err != nil {
		return err
	}
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	added, err := appendDeployAuthorizedKeys(user, keys)
	if err != nil {
		return err
	}
	if added == 0 {
		fmt.Printf("SSH key already authorized for %s (%s); no changes\n", user, comment)
		return nil
	}
	if added == 1 {
		fmt.Printf("Authorized 1 SSH key for %s (%s)\n", user, comment)
		return nil
	}
	fmt.Printf("Authorized %d SSH keys for %s (%s)\n", added, user, comment)
	return nil
}

func deployAuthorizedKeysUser() (string, error) {
	user, err := host.DeployUserFromSudo()
	if err != nil {
		return "", err
	}
	if user != "" {
		return user, nil
	}
	file, err := store.Default().ReadHost()
	if err == nil && file.Desired.Users.Deploy != "" {
		return file.Desired.Users.Deploy, nil
	}
	return "", fmt.Errorf("deploy user is unknown; run through sudo as the deploy user or run ship box init")
}

func appendDeployAuthorizedKeys(user string, keys []authorizedKey) (int, error) {
	home, err := homeDirForUser(user)
	if err != nil {
		return 0, err
	}
	sshDir := filepath.Join(home, ".ssh")
	path := filepath.Join(sshDir, "authorized_keys")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return 0, fmt.Errorf("create %s: %v", sshDir, err)
	}
	existing, err := readAuthorizedKeys(path)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	var lines []string
	for _, key := range existing {
		lines = append(lines, key.Line)
		if key.Material != "" {
			seen[key.Material] = true
		}
	}
	added := 0
	for _, key := range keys {
		if seen[key.Material] {
			continue
		}
		lines = append(lines, key.Line)
		seen[key.Material] = true
		added++
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := store.AtomicWrite(path, []byte(content), 0600); err != nil {
		return 0, fmt.Errorf("write %s: %v", path, err)
	}
	if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, sshDir}, ""); err != nil {
		return 0, fmt.Errorf("chown %s: %v", sshDir, err)
	}
	if err := os.Chmod(sshDir, 0700); err != nil {
		return 0, fmt.Errorf("chmod %s: %v", sshDir, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return 0, fmt.Errorf("chmod %s: %v", path, err)
	}
	return added, nil
}

func readAuthorizedKeys(path string) ([]authorizedKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %v", path, err)
	}
	var keys []authorizedKey
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, err := parseAuthorizedKeyLine(line)
		if err != nil {
			key = authorizedKey{Line: line}
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func normalizeAuthorizedKeys(raw, comment string) ([]authorizedKey, error) {
	var keys []authorizedKey
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := normalizeAuthorizedKeyLine(line, comment)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no SSH public keys provided")
	}
	return keys, nil
}

func normalizeAuthorizedKeyLine(line, comment string) (authorizedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return authorizedKey{}, fmt.Errorf("public key line must contain key type and key body")
	}
	if !supportedAuthorizedKeyType(fields[0]) {
		return authorizedKey{}, fmt.Errorf("unsupported public key type %q", fields[0])
	}
	if fields[1] == "" {
		return authorizedKey{}, fmt.Errorf("public key body is empty")
	}
	line = fields[0] + " " + fields[1] + " " + comment
	return authorizedKey{Line: line, Material: keyMaterial(fields[0], fields[1])}, nil
}

func parseAuthorizedKeyLine(line string) (authorizedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || !supportedAuthorizedKeyType(fields[0]) {
		return authorizedKey{}, fmt.Errorf("not a plain SSH public key")
	}
	return authorizedKey{Line: line, Material: keyMaterial(fields[0], fields[1])}, nil
}

func supportedAuthorizedKeyType(value string) bool {
	switch value {
	case "ssh-ed25519", "ssh-rsa",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return false
	}
}

func keyMaterial(kind, body string) string {
	return kind + "\x00" + body
}

func homeDirForUser(user string) (string, error) {
	out, err := exec.Command("getent", "passwd", user).Output()
	if err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), ":")
		if len(parts) >= 6 && parts[5] != "" {
			return parts[5], nil
		}
	}
	if user == "" || strings.Contains(user, "/") {
		return "", fmt.Errorf("invalid deploy user %q", user)
	}
	return filepath.Join("/home", user), nil
}
