package helper

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/host"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

type keyCmd struct {
	Add keyAddCmd  `cmd:"add" help:"Append SSH public keys to the deploy user's authorized_keys."`
	Ls  keyListCmd `cmd:"ls" help:"List deploy SSH keys."`
	Rm  keyRmCmd   `cmd:"rm" help:"Remove deploy SSH keys by member name."`
}

type keyAddCmd struct {
	Comment string `name:"comment" required:"" help:"Comment to stamp on appended keys."`
}

type keyListCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of plain text."`
}

type keyRmCmd struct {
	Name string `arg:"" help:"Member name whose keys should be removed."`
}

type authorizedKey struct {
	Line        string
	Material    string
	Type        string
	Body        string
	Comment     string
	Fingerprint string
}

type keyAddResult struct {
	Key   authorizedKey
	Added bool
}

type memberKeyRow struct {
	Name        string `json:"name"`
	KeyType     string `json:"key_type"`
	Fingerprint string `json:"fingerprint"`
}

type memberKeyListPayload struct {
	Members []memberKeyRow `json:"members"`
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
	results, err := appendDeployAuthorizedKeys(user, keys)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Println(formatKeyAddResult(result))
	}
	return nil
}

func (c keyListCmd) Run() error {
	if err := c.run(); err != nil {
		utils.DieError(err, 1)
	}
	return nil
}

func (c keyListCmd) run() error {
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return err
	}
	rows := memberRows(keys)
	if c.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(memberKeyListPayload{Members: rows})
	}
	for _, row := range rows {
		fmt.Println(formatMemberRow(row))
	}
	return nil
}

func (c keyRmCmd) Run() error {
	if err := c.run(); err != nil {
		utils.DieError(err, 1)
	}
	return nil
}

func (c keyRmCmd) run() error {
	name := strings.Join(strings.Fields(c.Name), " ")
	if name == "" {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "member name is required",
			"command": "ship member rm <name>",
		})
	}
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	removed, err := removeDeployAuthorizedKeys(user, name)
	if err != nil {
		return err
	}
	if removed == 1 {
		fmt.Printf("removed 1 SSH key for %s\n", name)
		return nil
	}
	fmt.Printf("removed %d SSH keys for %s\n", removed, name)
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
	return "", fmt.Errorf("deploy user is unknown; run through sudo as the deploy user or run ship box setup")
}

func appendDeployAuthorizedKeys(user string, keys []authorizedKey) ([]keyAddResult, error) {
	sshDir, path, err := deployAuthorizedKeysPaths(user)
	if err != nil {
		return nil, err
	}
	if err := ensureDeploySSHDir(sshDir); err != nil {
		return nil, err
	}
	existing, err := readAuthorizedKeys(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var lines []string
	for _, key := range existing {
		lines = append(lines, key.Line)
		if key.Material != "" {
			seen[key.Material] = true
		}
	}
	var results []keyAddResult
	for _, key := range keys {
		if seen[key.Material] {
			results = append(results, keyAddResult{Key: key})
			continue
		}
		lines = append(lines, key.Line)
		seen[key.Material] = true
		results = append(results, keyAddResult{Key: key, Added: true})
	}
	if err := writeDeployAuthorizedKeys(user, sshDir, path, lines); err != nil {
		return nil, err
	}
	return results, nil
}

func removeDeployAuthorizedKeys(user, name string) (int, error) {
	sshDir, path, err := deployAuthorizedKeysPaths(user)
	if err != nil {
		return 0, err
	}
	if err := ensureDeploySSHDir(sshDir); err != nil {
		return 0, err
	}
	existing, err := readAuthorizedKeys(path)
	if err != nil {
		return 0, err
	}
	parseable := 0
	removed := 0
	memberNames := map[string]bool{}
	var lines []string
	for _, key := range existing {
		if key.Material == "" {
			lines = append(lines, key.Line)
			continue
		}
		parseable++
		memberNames[key.Comment] = true
		if key.Comment == name {
			removed++
			continue
		}
		lines = append(lines, key.Line)
	}
	if removed == 0 {
		return 0, errcat.New(errcat.CodeMemberNotFound, errcat.Fields{
			"name":    name,
			"members": memberNamesList(memberNames),
		})
	}
	if parseable-removed == 0 {
		return 0, errcat.New(errcat.CodeMemberLastKey, errcat.Fields{"name": name})
	}
	if err := writeDeployAuthorizedKeys(user, sshDir, path, lines); err != nil {
		return 0, err
	}
	return removed, nil
}

func deployAuthorizedKeysPaths(user string) (string, string, error) {
	home, err := homeDirForUser(user)
	if err != nil {
		return "", "", err
	}
	sshDir := filepath.Join(home, ".ssh")
	return sshDir, filepath.Join(sshDir, "authorized_keys"), nil
}

func ensureDeploySSHDir(sshDir string) error {
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create %s: %v", sshDir, err)
	}
	return nil
}

func readDeployAuthorizedKeys(user string) ([]authorizedKey, error) {
	_, path, err := deployAuthorizedKeysPaths(user)
	if err != nil {
		return nil, err
	}
	return readAuthorizedKeys(path)
}

func writeDeployAuthorizedKeys(user, sshDir, path string, lines []string) error {
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := store.AtomicWrite(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("write %s: %v", path, err)
	}
	if _, err := utils.RunChecked("chown", []string{"-R", user + ":" + user, sshDir}, ""); err != nil {
		return fmt.Errorf("chown %s: %v", sshDir, err)
	}
	if err := os.Chmod(sshDir, 0700); err != nil {
		return fmt.Errorf("chmod %s: %v", sshDir, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("chmod %s: %v", path, err)
	}
	return nil
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
		return nil, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "no SSH public keys provided"})
	}
	return keys, nil
}

func normalizeAuthorizedKeyLine(line, comment string) (authorizedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return authorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key line must contain key type and key body"})
	}
	if !supportedAuthorizedKeyType(fields[0]) {
		return authorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("unsupported public key type %q", fields[0])})
	}
	if fields[1] == "" {
		return authorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key body is empty"})
	}
	fingerprint, err := publicKeyFingerprint(fields[1])
	if err != nil {
		return authorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": err.Error()})
	}
	comment = strings.Join(strings.Fields(comment), " ")
	if comment == "" {
		comment = "ship-member"
	}
	line = fields[0] + " " + fields[1] + " " + comment
	return authorizedKey{
		Line:        line,
		Material:    keyMaterial(fields[0], fields[1]),
		Type:        fields[0],
		Body:        fields[1],
		Comment:     comment,
		Fingerprint: fingerprint,
	}, nil
}

func parseAuthorizedKeyLine(line string) (authorizedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || !supportedAuthorizedKeyType(fields[0]) {
		return authorizedKey{}, fmt.Errorf("not a plain SSH public key")
	}
	fingerprint, err := publicKeyFingerprint(fields[1])
	if err != nil {
		return authorizedKey{}, err
	}
	comment := ""
	if len(fields) > 2 {
		comment = strings.Join(fields[2:], " ")
	}
	if comment == "" {
		comment = "unknown"
	}
	return authorizedKey{
		Line:        line,
		Material:    keyMaterial(fields[0], fields[1]),
		Type:        fields[0],
		Body:        fields[1],
		Comment:     comment,
		Fingerprint: fingerprint,
	}, nil
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

func publicKeyFingerprint(body string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("public key body is not valid base64")
	}
	if len(blob) == 0 {
		return "", fmt.Errorf("public key body is empty")
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func memberRows(keys []authorizedKey) []memberKeyRow {
	var rows []memberKeyRow
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		rows = append(rows, memberKeyRow{
			Name:        key.Comment,
			KeyType:     key.Type,
			Fingerprint: key.Fingerprint,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].KeyType != rows[j].KeyType {
			return rows[i].KeyType < rows[j].KeyType
		}
		return rows[i].Fingerprint < rows[j].Fingerprint
	})
	return rows
}

func formatKeyAddResult(result keyAddResult) string {
	line := formatMemberRow(memberKeyRow{
		Name:        result.Key.Comment,
		KeyType:     result.Key.Type,
		Fingerprint: result.Key.Fingerprint,
	})
	if result.Added {
		return "added " + line
	}
	return "skipped " + line + " (already authorized)"
}

func formatMemberRow(row memberKeyRow) string {
	return row.Name + " " + row.KeyType + " " + row.Fingerprint
}

func memberNamesList(names map[string]bool) string {
	if len(names) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
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
