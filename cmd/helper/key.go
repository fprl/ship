package helper

import (
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
	"github.com/fprl/ship/internal/memberkeys"
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

type authorizedKey = memberkeys.AuthorizedKey
type keyAddResult = memberkeys.AddResult
type memberKeyRow = memberkeys.Row

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
	lines, results := memberkeys.Merge(existing, keys)
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
	return memberkeys.Parse(data), nil
}

func normalizeAuthorizedKeys(raw, comment string) ([]authorizedKey, error) {
	return memberkeys.Normalize(raw, comment)
}

func memberRows(keys []authorizedKey) []memberKeyRow {
	return memberkeys.Rows(keys)
}

func publicKeyFingerprint(body string) (string, error) {
	return memberkeys.PublicKeyFingerprint(body)
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
