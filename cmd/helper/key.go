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
	MemberFingerprint string     `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Add               keyAddCmd  `cmd:"add" help:"Append SSH public keys to the deploy user's authorized_keys."`
	Ls                keyListCmd `cmd:"ls" help:"List deploy SSH keys."`
	Rm                keyRmCmd   `cmd:"rm" help:"Remove deploy SSH keys by member name."`
}

func (c keyCmd) BeforeApply() error {
	return requireRoot()
}

func (c keyCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

type keyAddCmd struct {
	Name string `name:"name" required:"" help:"Box-global member name."`
	Role string `name:"role" enum:"owner,shipper,agent" default:"shipper" help:"Role recorded for newly added keys."`
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
	name := strings.Join(strings.Fields(c.Name), " ")
	if name == "" {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "member name is required",
			"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name <name>",
		})
	}
	role := store.MemberRole(c.Role)
	if !store.ValidMemberRole(role) {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "role must be owner, shipper, or agent",
			"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name " + name + " --role owner|shipper|agent",
		})
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("read public keys from stdin: %v", err)
	}
	keys, err := normalizeAuthorizedKeys(string(raw), name)
	if err != nil {
		return err
	}
	authorizeOrDie(helperVerbMember, authTargetForBox("add member name="+name+" role="+string(role), memberAddTargetArgs(name, role, keys)...))
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	results, err := appendDeployAuthorizedKeys(user, keys, role)
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
	authorizeOrDie(helperVerbMember, authTargetForBox("list members"))
	rows, err := readMemberRows()
	if err != nil {
		return err
	}
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
			"command": "ship box member rm <name> " + boxClientAddress(),
		})
	}
	authorizeOrDie(helperVerbMember, authTargetForBox("rm member name="+name, "name="+name))
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
	if os.Getenv("SHIP_AUTHORIZED_KEYS_FILE") != "" {
		return "deploy", nil
	}
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

func appendDeployAuthorizedKeys(user string, keys []authorizedKey, role store.MemberRole) ([]keyAddResult, error) {
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
	members, err := store.Default().ReadMembers()
	if err != nil {
		return nil, err
	}
	existingRecords := memberkeys.EffectiveMemberRecords(existing, *members, nil)
	if err := validateMemberEnrollment(existing, existingRecords, keys, role); err != nil {
		return nil, err
	}
	lines, results := memberkeys.Merge(existing, keys)
	mergedKeys := memberkeys.Parse(memberkeys.Content(lines))
	overrides := map[string]store.MemberRecord{}
	for i := range results {
		result := &results[i]
		overrides[result.Key.Fingerprint] = store.MemberRecord{Name: result.Key.Comment, Role: role}
		result.Role = string(role)
	}
	records := memberkeys.EffectiveMemberRecords(mergedKeys, *members, overrides)
	rendered := memberkeys.RenderAuthorizedKeyLines(mergedKeys, records)
	if err := writeReconciledMembers(mergedKeys, *members, overrides); err != nil {
		return nil, err
	}
	if err := writeDeployAuthorizedKeys(user, sshDir, path, rendered); err != nil {
		return nil, err
	}
	return results, nil
}

func validateMemberEnrollment(existing []authorizedKey, existingRecords map[string]store.MemberRecord, keys []authorizedKey, role store.MemberRole) error {
	for _, key := range keys {
		for _, existingKey := range existing {
			if existingKey.Material == "" {
				continue
			}
			record, recorded := existingRecords[existingKey.Fingerprint]
			if existingKey.Material == key.Material {
				if !recorded {
					continue
				}
				if record.Name != key.Comment {
					return errcat.New(errcat.CodeUsageError, errcat.Fields{
						"detail":  fmt.Sprintf("key %s already belongs to member %q", key.Fingerprint, record.Name),
						"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name " + record.Name,
					})
				}
				if record.Role != role {
					return errcat.New(errcat.CodeUsageError, errcat.Fields{
						"detail":  fmt.Sprintf("member %q already has role %q; additional keys must use that role", key.Comment, record.Role),
						"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name " + key.Comment + " --role " + string(record.Role),
					})
				}
				continue
			}
			if !recorded {
				continue
			}
			if record.Name == key.Comment && record.Role != role {
				return errcat.New(errcat.CodeUsageError, errcat.Fields{
					"detail":  fmt.Sprintf("member %q already has role %q; additional keys must use that role", key.Comment, record.Role),
					"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name " + key.Comment + " --role " + string(record.Role),
				})
			}
		}
	}
	return nil
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
	members, err := store.Default().ReadMembers()
	if err != nil {
		return 0, err
	}
	records := memberkeys.EffectiveMemberRecords(existing, *members, nil)
	removed := 0
	removedRecorded := 0
	memberNames := map[string]bool{}
	var lines []string
	for _, key := range existing {
		if key.Material == "" {
			lines = append(lines, key.Line)
			continue
		}
		memberName := key.Comment
		if record, ok := records[key.Fingerprint]; ok {
			memberName = record.Name
			memberNames[memberName] = true
		}
		if memberName == name {
			removed++
			if _, ok := records[key.Fingerprint]; ok {
				removedRecorded++
			}
			continue
		}
		lines = append(lines, key.Line)
	}
	if removed == 0 {
		return 0, errcat.New(errcat.CodeMemberNotFound, errcat.Fields{
			"name":    name,
			"members": memberNamesList(memberNames),
			"box":     boxClientAddress(),
		})
	}
	remaining := memberkeys.Parse(memberkeys.Content(lines))
	remainingRecords := memberkeys.EffectiveMemberRecords(remaining, *members, nil)
	if removedRecorded > 0 && len(remainingRecords) == 0 {
		return 0, errcat.New(errcat.CodeMemberLastKey, errcat.Fields{"name": name, "box": boxClientAddress()})
	}
	rendered := memberkeys.RenderAuthorizedKeyLines(remaining, remainingRecords)
	if err := writeDeployAuthorizedKeys(user, sshDir, path, rendered); err != nil {
		return 0, err
	}
	if err := writeReconciledMembers(remaining, *members, nil); err != nil {
		return 0, err
	}
	return removed, nil
}

func deployAuthorizedKeysPaths(user string) (string, string, error) {
	if path := os.Getenv("SHIP_AUTHORIZED_KEYS_FILE"); path != "" {
		return filepath.Dir(path), path, nil
	}
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
	keys, err := memberkeys.Normalize(raw, comment)
	if !errcat.Is(err, errcat.CodeSSHPublicKeyInvalid) {
		return keys, err
	}
	coded, _ := errcat.As(err)
	return nil, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{
		"detail": coded.Cause(),
		"box":    boxClientAddress(),
	})
}

func memberRows(keys []authorizedKey, members store.MembersFile) []memberKeyRow {
	return memberkeys.RowsWithMembers(keys, members)
}

func readMemberRows() ([]memberKeyRow, error) {
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return nil, err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return nil, err
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return nil, err
	}
	return memberRows(keys, *members), nil
}

func formatKeyAddResult(result keyAddResult) string {
	role := result.Role
	if role == "" {
		role = string(store.MemberRoleShipper)
	}
	if result.Added {
		return fmt.Sprintf("member added: %s (%s, %s)", result.Key.Comment, role, result.Key.Fingerprint)
	}
	return fmt.Sprintf("member %s already authorized (%s, %s)", result.Key.Comment, role, result.Key.Fingerprint)
}

func formatMemberRow(row memberKeyRow) string {
	return row.Name + " " + row.Role + " " + row.KeyType + " " + row.Fingerprint
}

func memberAddTargetArgs(name string, role store.MemberRole, keys []authorizedKey) []string {
	args := []string{"name=" + name, "role=" + string(role)}
	for _, key := range keys {
		args = append(args, "fingerprint="+key.Fingerprint)
	}
	return args
}

func writeReconciledMembers(keys []authorizedKey, current store.MembersFile, overrides map[string]store.MemberRecord) error {
	file := memberkeys.ReconciledMembersFile(keys, current, overrides)
	return store.Default().WriteMembers(file)
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
