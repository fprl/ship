package memberkeys

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
	"golang.org/x/crypto/ssh"
)

type AuthorizedKey struct {
	Line        string
	Material    string
	Type        string
	Body        string
	Comment     string
	Fingerprint string
}

type AddResult struct {
	Key   AuthorizedKey
	Added bool
	Role  string
}

type Row struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	KeyType     string `json:"key_type"`
	Fingerprint string `json:"fingerprint"`
	KeyID       string `json:"-"`
	Body        string `json:"-"`
	Current     bool   `json:"-"`
}

const MinKeySelectorLength = 12

func Normalize(raw, comment string) ([]AuthorizedKey, error) {
	var keys []AuthorizedKey
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := NormalizeLine(line, comment)
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

func NormalizeLine(line, comment string) (AuthorizedKey, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return AuthorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key line must contain key type and key body"})
	}
	if !SupportedType(fields[0]) {
		return AuthorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": fmt.Sprintf("unsupported public key type %q", fields[0])})
	}
	if fields[1] == "" {
		return AuthorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": "public key body is empty"})
	}
	fingerprint, err := PublicKeyFingerprint(fields[0], fields[1])
	if err != nil {
		return AuthorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": err.Error()})
	}
	comment = strings.Join(strings.Fields(comment), " ")
	if comment == "" && len(fields) > 2 {
		comment = strings.Join(fields[2:], " ")
	}
	comment = strings.Join(strings.Fields(comment), " ")
	if comment == "" {
		comment = "ship-member"
	}
	line = fields[0] + " " + fields[1] + " " + comment
	return AuthorizedKey{
		Line:        line,
		Material:    KeyMaterial(fields[0], fields[1]),
		Type:        fields[0],
		Body:        fields[1],
		Comment:     comment,
		Fingerprint: fingerprint,
	}, nil
}

func ParseLine(line string) (AuthorizedKey, error) {
	_, rest, err := splitAuthorizedKeyLine(line)
	if err != nil {
		return AuthorizedKey{}, err
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 || !SupportedType(fields[0]) {
		return AuthorizedKey{}, fmt.Errorf("not a plain SSH public key")
	}
	fingerprint, err := PublicKeyFingerprint(fields[0], fields[1])
	if err != nil {
		return AuthorizedKey{}, errcat.New(errcat.CodeSSHPublicKeyInvalid, errcat.Fields{"detail": err.Error()})
	}
	comment := ""
	if len(fields) > 2 {
		comment = strings.Join(fields[2:], " ")
	}
	if comment == "" {
		comment = "unknown"
	}
	return AuthorizedKey{
		Line:        line,
		Material:    KeyMaterial(fields[0], fields[1]),
		Type:        fields[0],
		Body:        fields[1],
		Comment:     comment,
		Fingerprint: fingerprint,
	}, nil
}

func Parse(content []byte) []AuthorizedKey {
	var keys []AuthorizedKey
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, err := ParseLine(line)
		if err != nil {
			key = AuthorizedKey{Line: line}
		}
		keys = append(keys, key)
	}
	return keys
}

func Merge(existing []AuthorizedKey, keys []AuthorizedKey) ([]string, []AddResult) {
	seen := map[string]bool{}
	var lines []string
	for _, key := range existing {
		lines = append(lines, key.Line)
		if key.Material != "" {
			seen[key.Material] = true
		}
	}
	var results []AddResult
	for _, key := range keys {
		if seen[key.Material] {
			results = append(results, AddResult{Key: key})
			continue
		}
		lines = append(lines, key.Line)
		seen[key.Material] = true
		results = append(results, AddResult{Key: key, Added: true})
	}
	return lines, results
}

func Content(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func RenderAuthorizedKeyLines(keys []AuthorizedKey, records map[string]store.MemberRecord) []string {
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		record, ok := records[key.Fingerprint]
		if !ok {
			continue
		}
		lines = append(lines, RenderAuthorizedKeyLine(key, record))
	}
	return lines
}

func RenderAuthorizedKeyLine(key AuthorizedKey, record store.MemberRecord) string {
	name := strings.Join(strings.Fields(record.Name), " ")
	if name == "" {
		name = key.Comment
	}
	role := record.Role
	if !store.ValidMemberRole(role) {
		role = store.MemberRoleShipper
	}
	public := key.Type + " " + key.Body
	if name != "" {
		public += " " + name
	}
	if role != store.MemberRoleAgent {
		return public
	}
	return fmt.Sprintf("command=\"/usr/local/bin/ship server agent-shell --member-fingerprint %s\",restrict %s", key.Fingerprint, public)
}

func RowsWithMembers(keys []AuthorizedKey, members store.MembersFile, current ...string) []Row {
	records := EffectiveMemberRecords(keys, members, nil)
	rows := make([]Row, 0, len(keys))
	currentFingerprint := ""
	if len(current) > 0 {
		currentFingerprint = current[0]
	}
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		record, ok := records[key.Fingerprint]
		if !ok {
			continue
		}
		rows = append(rows, Row{
			Name:        record.Name,
			Role:        string(record.Role),
			KeyType:     key.Type,
			Fingerprint: key.Fingerprint,
			Body:        key.Body,
			Current:     key.Fingerprint == currentFingerprint,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].Role != rows[j].Role {
			return rows[i].Role < rows[j].Role
		}
		if rows[i].KeyType != rows[j].KeyType {
			return rows[i].KeyType < rows[j].KeyType
		}
		return rows[i].Fingerprint < rows[j].Fingerprint
	})
	return rows
}

// ShortestUniqueKeyIDs returns copy-pasteable ids for the fingerprint
// payloads. The floor is deliberate: short prefixes are too easy to collide
// on a real box.
func ShortestUniqueKeyIDs(keys []AuthorizedKey) map[string]string {
	ids := make(map[string]string)
	for i, key := range keys {
		payload, ok := fingerprintPayload(key.Fingerprint)
		if !ok || payload == "" {
			continue
		}
		length := len(payload)
		for candidate := MinKeySelectorLength; candidate < len(payload); candidate++ {
			prefix := payload[:candidate]
			unique := true
			for j, other := range keys {
				otherPayload, otherOK := fingerprintPayload(other.Fingerprint)
				if i != j && otherOK && strings.HasPrefix(otherPayload, prefix) {
					unique = false
					break
				}
			}
			if unique {
				length = candidate
				break
			}
		}
		ids[key.Fingerprint] = "SHA256:" + payload[:length]
	}
	return ids
}

func fingerprintPayload(fingerprint string) (string, bool) {
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		return "", false
	}
	return strings.TrimPrefix(fingerprint, "SHA256:"), true
}

// ResolveKeySelector resolves a full fingerprint or a unique fingerprint
// payload prefix. It does not inspect member records, so callers can use it
// without leaking another member's identity in a belongs-to check.
func ResolveKeySelector(keys []AuthorizedKey, selector string) ([]AuthorizedKey, error) {
	selector = strings.TrimSpace(selector)
	for _, key := range keys {
		if key.Fingerprint == selector {
			return []AuthorizedKey{key}, nil
		}
	}
	payloadSelector := selector
	if strings.HasPrefix(payloadSelector, "SHA256:") {
		payloadSelector = strings.TrimPrefix(payloadSelector, "SHA256:")
	}
	if len(payloadSelector) < MinKeySelectorLength {
		return nil, errcat.New(errcat.CodeUsageError, errcat.Fields{
			"detail":  fmt.Sprintf("key selector must be at least %d characters", MinKeySelectorLength),
			"command": "ship box member ls {box}",
		})
	}
	var matches []AuthorizedKey
	for _, key := range keys {
		payload, ok := fingerprintPayload(key.Fingerprint)
		if ok && strings.HasPrefix(payload, payloadSelector) {
			matches = append(matches, key)
		}
	}
	switch len(matches) {
	case 0:
		return nil, errcat.New(errcat.CodeMemberKeyNotFound, errcat.Fields{"selector": selector})
	case 1:
		return matches, nil
	default:
		fingerprints := make([]string, 0, len(matches))
		for _, key := range matches {
			fingerprints = append(fingerprints, key.Fingerprint)
		}
		sort.Strings(fingerprints)
		return nil, errcat.New(errcat.CodeMemberKeyAmbiguous, errcat.Fields{
			"selector": selector,
			"matches":  strings.Join(fingerprints, ", "),
		})
	}
}

// ValidateEffectiveOwner is the single guard used by every member
// mutation. An owner record that has no matching authorized_keys line is not
// effective and therefore cannot satisfy the invariant.
func ValidateEffectiveOwner(keys []AuthorizedKey, records map[string]store.MemberRecord, box string) error {
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		if record, ok := records[key.Fingerprint]; ok && record.Role == store.MemberRoleOwner {
			return nil
		}
	}
	return errcat.New(errcat.CodeMemberLastOwner, errcat.Fields{"box": box})
}

func ReconciledMembersFile(keys []AuthorizedKey, current store.MembersFile, overrides map[string]store.MemberRecord) store.MembersFile {
	return store.MembersFile{
		Version: store.CurrentVersion,
		Members: EffectiveMemberRecords(keys, current, overrides),
	}
}

func EffectiveMemberRecords(keys []AuthorizedKey, members store.MembersFile, overrides map[string]store.MemberRecord) map[string]store.MemberRecord {
	records := map[string]store.MemberRecord{}
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		record, ok := members.Members[key.Fingerprint]
		if override, found := overrides[key.Fingerprint]; found {
			record = override
			ok = true
		}
		if !ok {
			continue
		}
		records[key.Fingerprint] = record
	}
	return records
}

func SupportedType(value string) bool {
	switch value {
	case "ssh-ed25519", "ssh-rsa",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return true
	default:
		return false
	}
}

func splitAuthorizedKeyLine(line string) (string, string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", fmt.Errorf("empty authorized key line")
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 && SupportedType(fields[0]) {
		return "", line, nil
	}
	inQuote := false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote || (i > 0 && !isSpace(line[i-1])) {
			continue
		}
		for _, kind := range supportedTypes() {
			if strings.HasPrefix(line[i:], kind) {
				end := i + len(kind)
				if end < len(line) && isSpace(line[end]) {
					return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i:]), nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("not a plain SSH public key")
}

func supportedTypes() []string {
	return []string{
		"ssh-ed25519", "ssh-rsa",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com",
	}
}

func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func KeyMaterial(kind, body string) string {
	return kind + "\x00" + body
}

func PublicKeyFingerprint(kind, body string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("public key body is not valid base64")
	}
	if len(blob) == 0 {
		return "", fmt.Errorf("public key body is empty")
	}
	key, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return "", fmt.Errorf("public key body is not a valid SSH public key")
	}
	if key.Type() != kind {
		return "", fmt.Errorf("public key type %q does not match declared type %q", key.Type(), kind)
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}
