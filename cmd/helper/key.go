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
	MemberFingerprint string       `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	Add               keyAddCmd    `cmd:"add" help:"Append SSH public keys to the deploy user's authorized_keys."`
	Ls                keyListCmd   `cmd:"ls" help:"List deploy SSH keys."`
	Rm                keyRmCmd     `cmd:"rm" help:"Remove deploy SSH keys by member name."`
	Rename            keyRenameCmd `cmd:"rename" help:"Rename a deploy member."`
	Role              keyRoleCmd   `cmd:"role" help:"Change a deploy member's role."`
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
	Key  string `name:"key" help:"Full fingerprint or unique fingerprint-payload prefix."`
}

type keyRenameCmd struct {
	Old string `arg:"" help:"Current member name."`
	New string `arg:"" help:"New member name."`
}

type keyRoleCmd struct {
	Name string `arg:"" help:"Member name."`
	Role string `arg:"" enum:"owner,shipper,agent" help:"New member role."`
}

type authorizedKey = memberkeys.AuthorizedKey
type keyAddResult = memberkeys.AddResult
type memberKeyRow = memberkeys.Row

type memberKeyListPayload struct {
	Members []memberListMember `json:"members"`
}

type memberListMember struct {
	Name string          `json:"name"`
	Role string          `json:"role"`
	Keys []memberListKey `json:"keys"`
}

type memberListKey struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Current     bool   `json:"current"`
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
	hadMember, oldIDs := existingMemberRotationKeys(user, name)
	results, err := appendDeployAuthorizedKeys(user, keys, role)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Println(formatKeyAddResult(result))
	}
	if hadMember {
		added := false
		for _, result := range results {
			added = added || result.Added
		}
		if added {
			fmt.Fprintln(os.Stderr, "rotation: verify a fresh connection with the new key before retiring an old key")
			currentKeys, readErr := readDeployAuthorizedKeys(user)
			if readErr != nil {
				return readErr
			}
			ids := memberkeys.ShortestUniqueKeyIDs(currentKeys)
			for _, fingerprint := range oldIDs {
				fmt.Fprintf(os.Stderr, "next: ship box member rm %s --key %s %s\n", name, ids[fingerprint], boxClientAddress())
			}
		}
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
	authorizeOrDie(helperVerbRead, authTargetForBox("list members"))
	rows, err := readMemberRows()
	if err != nil {
		return err
	}
	if c.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(memberKeyListPayload{Members: groupMemberRows(rows)})
	}
	if len(rows) > 0 {
		fmt.Println("NAME ROLE KEY-ID TYPE CURRENT")
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
	authorizeOrDie(helperVerbMember, authTargetForBox("rm member name="+name, "name="+name, "key="+c.Key))
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	removed, err := removeDeployAuthorizedKeys(user, name, c.Key)
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

func (c keyRenameCmd) Run() error {
	if err := c.run(); err != nil {
		utils.DieError(err, 1)
	}
	return nil
}

func (c keyRenameCmd) run() error {
	oldName := strings.Join(strings.Fields(c.Old), " ")
	newName := strings.Join(strings.Fields(c.New), " ")
	if oldName == "" || newName == "" {
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  "member rename requires <old> <new>",
			"command": "ship box member rename " + nonEmptyOr(oldName, "<old>") + " " + nonEmptyOr(newName, "<new>") + " " + boxClientAddress(),
		})
	}
	authorizeOrDie(helperVerbMember, authTargetForBox("rename member "+oldName+" to "+newName, "old="+oldName, "new="+newName))
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return err
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return err
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	if !memberNameExists(records, oldName) {
		return unknownMemberMutationError(oldName, records)
	}
	if oldName == newName {
		if !authorizedKeysMatchRendering(keys, records) {
			next := memberkeys.ReconciledMembersFile(keys, *members, nil)
			if err := memberkeys.ValidateEffectiveOwner(keys, next.Members, boxClientAddress()); err != nil {
				return err
			}
			if err := writeMemberGrant(user, keys, next); err != nil {
				return err
			}
		}
		fmt.Printf("member %s already named %s\n", oldName, newName)
		return nil
	}
	if memberNameExists(records, newName) {
		return errcat.New(errcat.CodeMemberNameTaken, errcat.Fields{"name": newName, "box": boxClientAddress()})
	}
	overrides := map[string]store.MemberRecord{}
	for fingerprint, record := range records {
		if normalizedMemberName(record.Name) == oldName {
			record.Name = newName
			overrides[fingerprint] = record
		}
	}
	next := memberkeys.ReconciledMembersFile(keys, *members, overrides)
	if err := memberkeys.ValidateEffectiveOwner(keys, next.Members, boxClientAddress()); err != nil {
		return err
	}
	if err := writeMemberGrant(user, keys, next); err != nil {
		return err
	}
	fmt.Printf("member renamed: %s -> %s\n", oldName, newName)
	return nil
}

func (c keyRoleCmd) Run() error {
	if err := c.run(); err != nil {
		utils.DieError(err, 1)
	}
	return nil
}

func (c keyRoleCmd) run() error {
	name := strings.Join(strings.Fields(c.Name), " ")
	role := store.MemberRole(c.Role)
	if name == "" || !store.ValidMemberRole(role) {
		detail := "member role requires <name> <owner|shipper|agent>"
		if name != "" {
			detail = "role must be owner, shipper, or agent"
		}
		return errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  detail,
			"command": "ship box member role " + nonEmptyOr(name, "<name>") + " " + nonEmptyOr(string(role), "<role>") + " " + boxClientAddress(),
		})
	}
	authorizeOrDie(helperVerbMember, authTargetForBox("role member name="+name+" role="+string(role), "name="+name, "role="+string(role)))
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return err
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return err
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	if !memberNameExists(records, name) {
		return unknownMemberMutationError(name, records)
	}
	if memberRoleAndRenderingMatch(name, role, keys, records) {
		fmt.Printf("member %s already role %s\n", name, role)
		return nil
	}
	overrides := map[string]store.MemberRecord{}
	for fingerprint, record := range records {
		if normalizedMemberName(record.Name) == name {
			record.Role = role
			overrides[fingerprint] = record
		}
	}
	next := memberkeys.ReconciledMembersFile(keys, *members, overrides)
	if err := memberkeys.ValidateEffectiveOwner(keys, next.Members, boxClientAddress()); err != nil {
		return err
	}
	if err := writeMemberMutationForRole(user, keys, next, role); err != nil {
		return err
	}
	fmt.Printf("member role changed: %s -> %s\n", name, role)
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
	return "deploy", nil
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
	if err := memberkeys.ValidateEffectiveOwner(mergedKeys, records, boxClientAddress()); err != nil {
		return nil, errcat.WithRemediation(err, "ship box setup "+boxClientAddress())
	}
	next := store.MembersFile{Version: store.CurrentVersion, Members: records}
	if err := writeMemberGrant(user, mergedKeys, next); err != nil {
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
				if normalizedMemberName(record.Name) != normalizedMemberName(key.Comment) {
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
			if normalizedMemberName(record.Name) == normalizedMemberName(key.Comment) && record.Role != role {
				return errcat.New(errcat.CodeUsageError, errcat.Fields{
					"detail":  fmt.Sprintf("member %q already has role %q; additional keys must use that role", key.Comment, record.Role),
					"command": "ship box member add <https-url|key|path> " + boxClientAddress() + " --name " + key.Comment + " --role " + string(record.Role),
				})
			}
		}
	}
	return nil
}

func removeDeployAuthorizedKeys(user, name string, selector ...string) (int, error) {
	keySelector := ""
	if len(selector) > 0 {
		keySelector = selector[0]
	}
	return removeDeployAuthorizedKeysWithSelector(user, name, keySelector)
}

func removeDeployAuthorizedKeysWithSelector(user, name, selector string) (int, error) {
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
	if selector != "" {
		matches, resolveErr := memberkeys.ResolveKeySelector(existing, selector, name)
		if resolveErr != nil {
			if errcat.Is(resolveErr, errcat.CodeUsageError) {
				return 0, errcat.New(errcat.CodeUsageError, errcat.Fields{
					"detail":  fmt.Sprintf("key selector must be at least %d characters", memberkeys.MinKeySelectorLength),
					"command": "ship box member ls " + boxClientAddress(),
				})
			}
			if errcat.Is(resolveErr, errcat.CodeMemberKeyAmbiguous) {
				coded, _ := errcat.As(resolveErr)
				cause := coded.Cause()
				prefix := "key selector " + selector + " matches multiple keys: "
				return 0, errcat.New(errcat.CodeMemberKeyAmbiguous, errcat.Fields{
					"selector": selector,
					"matches":  strings.TrimPrefix(cause, prefix),
					"name":     name,
					"box":      boxClientAddress(),
				})
			}
			return 0, errcat.New(errcat.CodeMemberKeyNotFound, errcat.Fields{"name": name, "box": boxClientAddress()})
		}
		key := matches[0]
		record, ok := records[key.Fingerprint]
		if !ok || normalizedMemberName(record.Name) != name {
			return 0, errcat.New(errcat.CodeMemberKeyNotFound, errcat.Fields{"name": name, "box": boxClientAddress()})
		}
		remaining := make([]authorizedKey, 0, len(existing)-1)
		for _, candidate := range existing {
			if candidate.Fingerprint != key.Fingerprint {
				remaining = append(remaining, candidate)
			}
		}
		if err := removeAuthorizedKeySet(user, remaining, *members); err != nil {
			return 0, err
		}
		return 1, nil
	}
	removed := 0
	var lines []string
	for _, key := range existing {
		if key.Material == "" {
			lines = append(lines, key.Line)
			continue
		}
		memberName := key.Comment
		if record, ok := records[key.Fingerprint]; ok {
			memberName = record.Name
		}
		if normalizedMemberName(memberName) == name {
			removed++
			continue
		}
		lines = append(lines, key.Line)
	}
	if removed == 0 {
		return 0, errcat.New(errcat.CodeMemberNotFound, errcat.Fields{
			"name":    name,
			"members": memberNamesList(memberNamesFromRecords(records)),
			"box":     boxClientAddress(),
		})
	}
	remaining := memberkeys.Parse(memberkeys.Content(lines))
	if err := removeAuthorizedKeySet(user, remaining, *members); err != nil {
		return 0, err
	}
	return removed, nil
}

func removeAuthorizedKeySet(user string, remaining []authorizedKey, current store.MembersFile) error {
	next := memberkeys.ReconciledMembersFile(remaining, current, nil)
	if err := memberkeys.ValidateEffectiveOwner(remaining, next.Members, boxClientAddress()); err != nil {
		return err
	}
	sshDir, path, err := deployAuthorizedKeysPaths(user)
	if err != nil {
		return err
	}
	rendered := memberkeys.RenderAuthorizedKeyLines(remaining, next.Members)
	return writeMemberRevocation(user, sshDir, path, rendered, next)
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
		"name":   normalizedMemberName(comment),
	})
}

func memberRows(keys []authorizedKey, members store.MembersFile) []memberKeyRow {
	rows := memberkeys.RowsWithMembers(keys, members, serverMemberFingerprint)
	ids := memberkeys.ShortestUniqueKeyIDs(keys)
	for i := range rows {
		rows[i].KeyID = ids[rows[i].Fingerprint]
	}
	return rows
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
	current := ""
	if row.Current {
		current = "CURRENT"
	}
	return fmt.Sprintf("%-20s %-8s %-19s %-28s %s", row.Name, row.Role, row.KeyID, row.KeyType, current)
}

func groupMemberRows(rows []memberKeyRow) []memberListMember {
	groups := make(map[string]*memberListMember)
	var order []string
	for _, row := range rows {
		group, ok := groups[row.Name]
		if !ok {
			group = &memberListMember{Name: row.Name, Role: row.Role, Keys: []memberListKey{}}
			groups[row.Name] = group
			order = append(order, row.Name)
		}
		group.Keys = append(group.Keys, memberListKey{ID: row.KeyID, Fingerprint: row.Fingerprint, Type: row.KeyType, Current: row.Current})
	}
	sort.Strings(order)
	out := make([]memberListMember, 0, len(order))
	for _, name := range order {
		out = append(out, *groups[name])
	}
	return out
}

func memberNameExists(records map[string]store.MemberRecord, name string) bool {
	for _, record := range records {
		if normalizedMemberName(record.Name) == normalizedMemberName(name) {
			return true
		}
	}
	return false
}

func memberNamesFromRecords(records map[string]store.MemberRecord) map[string]bool {
	names := make(map[string]bool)
	for _, record := range records {
		names[record.Name] = true
	}
	return names
}

func unknownMemberMutationError(name string, records map[string]store.MemberRecord) error {
	return errcat.New(errcat.CodeMemberNotFound, errcat.Fields{
		"name":    name,
		"members": memberNamesList(memberNamesFromRecords(records)),
		"box":     boxClientAddress(),
	})
}

func nonEmptyOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeMemberGrant(user string, keys []authorizedKey, next store.MembersFile) error {
	sshDir, path, err := deployAuthorizedKeysPaths(user)
	if err != nil {
		return err
	}
	if err := store.Default().WriteMembers(next); err != nil {
		return err
	}
	rendered := memberkeys.RenderAuthorizedKeyLines(keys, next.Members)
	return writeDeployAuthorizedKeys(user, sshDir, path, rendered)
}

func writeMemberRevocation(user, sshDir, path string, rendered []string, next store.MembersFile) error {
	if err := writeDeployAuthorizedKeys(user, sshDir, path, rendered); err != nil {
		return err
	}
	return store.Default().WriteMembers(next)
}

func writeMemberMutationForRole(user string, keys []authorizedKey, next store.MembersFile, role store.MemberRole) error {
	if role == store.MemberRoleAgent {
		sshDir, path, err := deployAuthorizedKeysPaths(user)
		if err != nil {
			return err
		}
		return writeMemberRevocation(user, sshDir, path, memberkeys.RenderAuthorizedKeyLines(keys, next.Members), next)
	}
	return writeMemberGrant(user, keys, next)
}

func memberRoleAndRenderingMatch(name string, role store.MemberRole, keys []authorizedKey, records map[string]store.MemberRecord) bool {
	found := false
	for _, record := range records {
		if normalizedMemberName(record.Name) != name {
			continue
		}
		found = true
		if record.Role != role {
			return false
		}
	}
	return found && authorizedKeysMatchRendering(keys, records)
}

func authorizedKeysMatchRendering(keys []authorizedKey, records map[string]store.MemberRecord) bool {
	expected := memberkeys.RenderAuthorizedKeyLines(keys, records)
	if len(keys) != len(expected) {
		return false
	}
	for i := range expected {
		if keys[i].Line != expected[i] {
			return false
		}
	}
	return true
}

func normalizedMemberName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

func existingMemberRotationKeys(user, name string) (bool, []string) {
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return false, nil
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return false, nil
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	var oldIDs []string
	for _, key := range keys {
		if record, ok := records[key.Fingerprint]; ok && normalizedMemberName(record.Name) == name {
			oldIDs = append(oldIDs, key.Fingerprint)
		}
	}
	sort.Strings(oldIDs)
	return len(oldIDs) > 0, oldIDs
}

func memberAddTargetArgs(name string, role store.MemberRole, keys []authorizedKey) []string {
	args := []string{"name=" + name, "role=" + string(role)}
	for _, key := range keys {
		args = append(args, "fingerprint="+key.Fingerprint)
	}
	return args
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
