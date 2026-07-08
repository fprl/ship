package memberkeys

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/store"
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
}

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
	fingerprint, err := PublicKeyFingerprint(fields[1])
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
	fields := strings.Fields(line)
	if len(fields) < 2 || !SupportedType(fields[0]) {
		return AuthorizedKey{}, fmt.Errorf("not a plain SSH public key")
	}
	fingerprint, err := PublicKeyFingerprint(fields[1])
	if err != nil {
		return AuthorizedKey{}, err
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

func Rows(keys []AuthorizedKey) []Row {
	var rows []Row
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		rows = append(rows, Row{
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

func RowsWithMembers(keys []AuthorizedKey, members store.MembersFile) []Row {
	records := EffectiveMemberRecords(keys, members, nil)
	var rows []Row
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		record := records[key.Fingerprint]
		rows = append(rows, Row{
			Name:        record.Name,
			Role:        string(record.Role),
			KeyType:     key.Type,
			Fingerprint: key.Fingerprint,
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

func ReconciledMembersFile(keys []AuthorizedKey, current store.MembersFile, overrides map[string]store.MemberRecord) store.MembersFile {
	return store.MembersFile{
		Version: store.CurrentVersion,
		Members: EffectiveMemberRecords(keys, current, overrides),
	}
}

func EffectiveMemberRecords(keys []AuthorizedKey, members store.MembersFile, overrides map[string]store.MemberRecord) map[string]store.MemberRecord {
	parseableCount := 0
	for _, key := range keys {
		if key.Material != "" {
			parseableCount++
		}
	}
	fallbackRole := store.MemberRoleShipper
	if parseableCount == 1 {
		fallbackRole = store.MemberRoleOwner
	}

	records := map[string]store.MemberRecord{}
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		record, ok := members.Members[key.Fingerprint]
		if !ok {
			record = store.MemberRecord{Name: key.Comment, Role: fallbackRole}
		}
		if override, ok := overrides[key.Fingerprint]; ok {
			record = override
		}
		record.Name = strings.Join(strings.Fields(record.Name), " ")
		if record.Name == "" {
			record.Name = key.Comment
		}
		if !store.ValidMemberRole(record.Role) {
			record.Role = fallbackRole
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

func KeyMaterial(kind, body string) string {
	return kind + "\x00" + body
}

func PublicKeyFingerprint(body string) (string, error) {
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
