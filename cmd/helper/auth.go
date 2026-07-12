package helper

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

const (
	approvalTTL = 15 * time.Minute

	helperVerbShip            helperVerb = "ship"
	helperVerbRollback        helperVerb = "rollback"
	helperVerbExec            helperVerb = "exec"
	helperVerbRead            helperVerb = "read"
	helperVerbSecretSet       helperVerb = "secret_set"
	helperVerbSecretRead      helperVerb = "secret_read"
	helperVerbSecretRemove    helperVerb = "secret_remove"
	helperVerbPreviewPin      helperVerb = "preview_pin"
	helperVerbPreviewPassword helperVerb = "preview_password"
	helperVerbData            helperVerb = "data"
	helperVerbRemoveEnv       helperVerb = "rm"
	helperVerbMember          helperVerb = "member"
	helperVerbBackupCreate    helperVerb = "save"
	helperVerbBackupRestore   helperVerb = "restore"
	helperVerbBoxMutation     helperVerb = "box_mutation"

	roleScopeAny     roleScope = "any"
	roleScopePreview roleScope = "preview"
)

type helperVerb string
type roleScope string

// helperRoleMatrix is the §13 role bundle encoded as data. Owners are
// intentionally omitted here because they are allowed everything.
var helperRoleMatrix = map[helperVerb]map[store.MemberRole]roleScope{
	helperVerbShip: {
		store.MemberRoleShipper: roleScopeAny,
		store.MemberRoleAgent:   roleScopePreview,
	},
	helperVerbRollback: {
		store.MemberRoleShipper: roleScopeAny,
		store.MemberRoleAgent:   roleScopePreview,
	},
	helperVerbExec: {
		store.MemberRoleShipper: roleScopeAny,
		store.MemberRoleAgent:   roleScopePreview,
	},
	helperVerbRead: {
		store.MemberRoleShipper: roleScopeAny,
		store.MemberRoleAgent:   roleScopeAny,
	},
	helperVerbSecretSet: {
		store.MemberRoleShipper: roleScopeAny,
	},
	helperVerbPreviewPin: {
		store.MemberRoleShipper: roleScopeAny,
		store.MemberRoleAgent:   roleScopePreview,
	},
	helperVerbPreviewPassword: {
		store.MemberRoleShipper: roleScopeAny,
	},
	helperVerbData: {
		store.MemberRoleShipper: roleScopeAny,
	},
	helperVerbRemoveEnv: {
		store.MemberRoleShipper: roleScopePreview,
	},
}

type authTarget struct {
	App     string
	Env     string
	Class   string
	Args    []string
	Summary string
}

type serverMember struct {
	Fingerprint string
	Name        string
	Role        store.MemberRole
}

var (
	serverMemberFingerprint string
	serverPinnedMemberName  string
	serverAuthorizedMember  *serverMember
	approvalNow             = func() time.Time { return time.Now().UTC() }
)

func setServerMemberClaims(fingerprint, member string) {
	serverMemberFingerprint = strings.TrimSpace(fingerprint)
	serverPinnedMemberName = strings.Join(strings.Fields(member), " ")
	serverAuthorizedMember = nil
}

func authorizeHelper(verb helperVerb, target authTarget) (serverMember, error) {
	member, err := resolveServerMember()
	if err != nil {
		return serverMember{}, err
	}
	serverAuthorizedMember = &member
	if helperAllows(member.Role, verb, target.Class) {
		return member, nil
	}
	if consumed, err := consumeApprovedRequest(member, verb, target); err != nil {
		return serverMember{}, err
	} else if consumed {
		return member, nil
	}
	request, minted, err := requestApproval(member, verb, target)
	if err != nil {
		return serverMember{}, err
	}
	if minted {
		appendApprovalJournalEntry("requested", request, member)
		notifyApprovalRequested(request, approvalNow())
	}
	return serverMember{}, approvalRequiredError(request)
}

func authorizeApprovalList() (serverMember, error) {
	member, err := resolveServerMember()
	if err != nil {
		return serverMember{}, err
	}
	serverAuthorizedMember = &member
	return member, nil
}

func authorizeApprovalGrant(id string) (serverMember, error) {
	member, err := resolveServerMember()
	if err != nil {
		return serverMember{}, err
	}
	serverAuthorizedMember = &member
	switch member.Role {
	case store.MemberRoleOwner, store.MemberRoleShipper:
		return member, nil
	default:
		return serverMember{}, errcat.New(errcat.CodeRoleDenied, errcat.Fields{
			"member":  member.Name,
			"role":    string(member.Role),
			"summary": "approve " + id,
			"command": "ask an owner or shipper to run ship approve " + id,
		})
	}
}

func authorizeOrDie(verb helperVerb, target authTarget) serverMember {
	member, err := authorizeHelper(verb, target)
	if err != nil {
		utilsDieError(err)
	}
	return member
}

func helperAllows(role store.MemberRole, verb helperVerb, class string) bool {
	if role == store.MemberRoleOwner {
		return true
	}
	scopes, ok := helperRoleMatrix[verb]
	if !ok {
		return false
	}
	scope, ok := scopes[role]
	if !ok {
		return false
	}
	switch scope {
	case roleScopeAny:
		return true
	case roleScopePreview:
		return class == "preview"
	default:
		return false
	}
}

func resolveServerMember() (serverMember, error) {
	if member := strings.TrimSpace(serverPinnedMemberName); member != "" {
		return resolvePinnedServerMember(member)
	}
	fingerprint := strings.TrimSpace(serverMemberFingerprint)
	if fingerprint == "" && os.Getenv("SUDO_USER") == "" {
		return serverMember{Name: "root", Role: store.MemberRoleOwner}, nil
	}
	if fingerprint == "" {
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": "(missing)"})
	}
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return serverMember{}, err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return serverMember{}, err
	}
	authorized := false
	for _, key := range keys {
		if key.Fingerprint == fingerprint {
			authorized = true
			break
		}
	}
	if !authorized {
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": fingerprint})
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return serverMember{}, err
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	record, ok := records[fingerprint]
	if !ok {
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": fingerprint})
	}
	return serverMember{
		Fingerprint: fingerprint,
		Name:        record.Name,
		Role:        record.Role,
	}, nil
}

func resolvePinnedServerMember(name string) (serverMember, error) {
	user, err := deployAuthorizedKeysUser()
	if err != nil {
		return serverMember{}, err
	}
	keys, err := readDeployAuthorizedKeys(user)
	if err != nil {
		return serverMember{}, err
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return serverMember{}, err
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	for _, key := range keys {
		if key.Material == "" {
			continue
		}
		record := records[key.Fingerprint]
		if record.Name != name {
			continue
		}
		return serverMember{
			Fingerprint: key.Fingerprint,
			Name:        record.Name,
			Role:        record.Role,
		}, nil
	}
	return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": "member:" + name})
}

func envClassForAuth(app, env string) string {
	if env == productionEnvName {
		return "production"
	}
	if file, err := readEnvIdentity(app, env); err == nil && file.Preview != nil {
		return "preview"
	}
	return "unknown"
}

func authTargetForAppEnv(app, env string, args ...string) authTarget {
	class := envClassForAuth(app, env)
	return authTarget{
		App:     app,
		Env:     env,
		Class:   class,
		Args:    append([]string(nil), args...),
		Summary: summarizeTarget(app, env, class, args...),
	}
}

func authTargetForPreviewBranch(app, branch string, args ...string) authTarget {
	summaryArgs := append([]string{"branch=" + branch}, args...)
	return authTarget{
		App:     app,
		Class:   "preview",
		Args:    summaryArgs,
		Summary: summarizeTarget(app, "", "preview", summaryArgs...),
	}
}

func authTargetForBox(summary string, args ...string) authTarget {
	return authTarget{
		Args:    append([]string(nil), args...),
		Summary: summary,
	}
}

func summarizeTarget(app, env, class string, args ...string) string {
	parts := []string{}
	if app != "" {
		parts = append(parts, "app="+app)
	}
	if env != "" {
		parts = append(parts, "env="+env)
	}
	if class != "" {
		parts = append(parts, "class="+class)
	}
	parts = append(parts, args...)
	if len(parts) == 0 {
		return "box"
	}
	return strings.Join(parts, " ")
}

func approvalMatchKey(member serverMember, verb helperVerb, target authTarget) string {
	payload := struct {
		Member string   `json:"member"`
		Verb   string   `json:"verb"`
		App    string   `json:"app,omitempty"`
		Env    string   `json:"env,omitempty"`
		Class  string   `json:"class,omitempty"`
		Args   []string `json:"args,omitempty"`
	}{
		Member: member.Fingerprint,
		Verb:   string(verb),
		App:    target.App,
		Env:    target.Env,
		Class:  target.Class,
		Args:   append([]string(nil), target.Args...),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func requestApproval(member serverMember, verb helperVerb, target authTarget) (store.ApprovalRequest, bool, error) {
	lock, err := acquireApprovalLock()
	if err != nil {
		return store.ApprovalRequest{}, false, err
	}
	defer lock.Release()

	now := approvalNow()
	file, err := store.Default().ReadApprovals()
	if err != nil {
		return store.ApprovalRequest{}, false, err
	}
	pruned := pruneExpiredApprovals(file, now)
	matchKey := approvalMatchKey(member, verb, target)
	for _, request := range file.Requests {
		if request.Status == store.ApprovalStatusPending && request.MatchKey == matchKey {
			if pruned {
				if err := store.Default().WriteApprovals(*file); err != nil {
					return store.ApprovalRequest{}, false, err
				}
			}
			return request, false, nil
		}
	}
	request := store.ApprovalRequest{
		ID: newApprovalID(file.Requests),
		Member: store.ApprovalMember{
			Fingerprint: member.Fingerprint,
			Name:        member.Name,
			Role:        member.Role,
		},
		Verb: string(verb),
		Target: store.ApprovalTarget{
			App:     target.App,
			Env:     target.Env,
			Class:   target.Class,
			Args:    append([]string(nil), target.Args...),
			Summary: target.Summary,
		},
		MatchKey:  matchKey,
		Status:    store.ApprovalStatusPending,
		CreatedAt: now.Format(time.RFC3339Nano),
		ExpiresAt: now.Add(approvalTTL).Format(time.RFC3339Nano),
	}
	file.Requests = append(file.Requests, request)
	sortApprovalRequests(file.Requests)
	return request, true, store.Default().WriteApprovals(*file)
}

func consumeApprovedRequest(member serverMember, verb helperVerb, target authTarget) (bool, error) {
	lock, err := acquireApprovalLock()
	if err != nil {
		return false, err
	}
	defer lock.Release()

	now := approvalNow()
	file, err := store.Default().ReadApprovals()
	if err != nil {
		return false, err
	}
	matchKey := approvalMatchKey(member, verb, target)
	for i, request := range file.Requests {
		if request.Status != store.ApprovalStatusApproved || request.MatchKey != matchKey {
			continue
		}
		if approvalExpired(request, now) {
			file.Requests = append(file.Requests[:i], file.Requests[i+1:]...)
			if err := store.Default().WriteApprovals(*file); err != nil {
				return false, err
			}
			return false, errcat.New(errcat.CodeApprovalExpired, errcat.Fields{
				"id":      request.ID,
				"summary": request.Target.Summary,
			})
		}
		file.Requests = append(file.Requests[:i], file.Requests[i+1:]...)
		if err := store.Default().WriteApprovals(*file); err != nil {
			return false, err
		}
		appendApprovalJournalEntry("consumed", request, member)
		return true, nil
	}
	pruned := pruneExpiredApprovals(file, now)
	if pruned {
		if err := store.Default().WriteApprovals(*file); err != nil {
			return false, err
		}
	}
	return false, nil
}

func approveRequest(id string, approver serverMember) (store.ApprovalRequest, error) {
	lock, err := acquireApprovalLock()
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	defer lock.Release()

	now := approvalNow()
	file, err := store.Default().ReadApprovals()
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	for i, request := range file.Requests {
		if request.ID != id {
			continue
		}
		if approvalExpired(request, now) {
			file.Requests = append(file.Requests[:i], file.Requests[i+1:]...)
			if err := store.Default().WriteApprovals(*file); err != nil {
				return store.ApprovalRequest{}, err
			}
			return store.ApprovalRequest{}, errcat.New(errcat.CodeApprovalExpired, errcat.Fields{
				"id":      request.ID,
				"summary": request.Target.Summary,
			})
		}
		if request.Status == store.ApprovalStatusApproved {
			return store.ApprovalRequest{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
				"detail":  "approval " + id + " is already approved",
				"command": "ship approve",
			})
		}
		request.Status = store.ApprovalStatusApproved
		request.ApprovedAt = now.Format(time.RFC3339Nano)
		request.ApprovedBy = &store.ApprovalMember{
			Fingerprint: approver.Fingerprint,
			Name:        approver.Name,
			Role:        approver.Role,
		}
		file.Requests[i] = request
		if err := store.Default().WriteApprovals(*file); err != nil {
			return store.ApprovalRequest{}, err
		}
		appendApprovalJournalEntry("approved", request, approver)
		return request, nil
	}
	pruned := pruneExpiredApprovals(file, now)
	if pruned {
		_ = store.Default().WriteApprovals(*file)
	}
	return store.ApprovalRequest{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "approval " + id + " was not found",
		"command": "ship approve",
	})
}

func pendingApprovals() ([]store.ApprovalRequest, error) {
	lock, err := acquireApprovalLock()
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	now := approvalNow()
	file, err := store.Default().ReadApprovals()
	if err != nil {
		return nil, err
	}
	pruned := pruneExpiredApprovals(file, now)
	if pruned {
		if err := store.Default().WriteApprovals(*file); err != nil {
			return nil, err
		}
	}
	var out []store.ApprovalRequest
	for _, request := range file.Requests {
		if request.Status == store.ApprovalStatusPending {
			out = append(out, request)
		}
	}
	sortApprovalRequests(out)
	return out, nil
}

func pruneExpiredApprovals(file *store.ApprovalsFile, now time.Time) bool {
	var kept []store.ApprovalRequest
	changed := false
	for _, request := range file.Requests {
		if approvalExpired(request, now) {
			changed = true
			continue
		}
		kept = append(kept, request)
	}
	if changed {
		file.Requests = kept
	}
	return changed
}

func approvalExpired(request store.ApprovalRequest, now time.Time) bool {
	expires, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
	if err != nil {
		return true
	}
	return !now.Before(expires)
}

func approvalRequiredError(request store.ApprovalRequest) error {
	return errcat.New(errcat.CodeApprovalRequired, errcat.Fields{
		"member":  request.Member.Name,
		"role":    string(request.Member.Role),
		"summary": request.Target.Summary,
		"id":      request.ID,
	})
}

func newApprovalID(existing []store.ApprovalRequest) string {
	seen := map[string]bool{}
	for _, request := range existing {
		seen[request.ID] = true
	}
	for {
		var raw [5]byte
		if _, err := rand.Read(raw[:]); err != nil {
			panic(fmt.Sprintf("generate approval id: %v", err))
		}
		id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))[:8]
		if !seen[id] {
			return id
		}
	}
}

func sortApprovalRequests(requests []store.ApprovalRequest) {
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].ExpiresAt != requests[j].ExpiresAt {
			return requests[i].ExpiresAt < requests[j].ExpiresAt
		}
		return requests[i].ID < requests[j].ID
	})
}

func acquireApprovalLock() (*appEnvLock, error) {
	return acquireLockFile(filepath.Join(appEnvLockDir(), "approvals.lock"))
}

type approvalJournalEntry struct {
	SchemaVersion int                  `json:"schema_version"`
	Event         string               `json:"event"`
	ID            string               `json:"id"`
	Member        store.ApprovalMember `json:"member"`
	Actor         store.ApprovalMember `json:"actor"`
	Verb          string               `json:"verb"`
	Target        store.ApprovalTarget `json:"target"`
	TS            string               `json:"ts"`
}

func appendApprovalJournalEntry(event string, request store.ApprovalRequest, actor serverMember) {
	entry := approvalJournalEntry{
		SchemaVersion: 1,
		Event:         event,
		ID:            request.ID,
		Member:        request.Member,
		Actor: store.ApprovalMember{
			Fingerprint: actor.Fingerprint,
			Name:        actor.Name,
			Role:        actor.Role,
		},
		Verb:   request.Verb,
		Target: request.Target,
		TS:     approvalNow().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := store.Default().ApprovalsJournalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

func currentServerMemberForJournal() *journalMember {
	if serverAuthorizedMember == nil {
		return nil
	}
	return &journalMember{
		Fingerprint: serverAuthorizedMember.Fingerprint,
		Name:        serverAuthorizedMember.Name,
		Role:        string(serverAuthorizedMember.Role),
	}
}

func utilsDieError(err error) {
	utils.DieError(err, 1)
}
