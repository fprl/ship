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

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/memberkeys"
	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
	"github.com/fprl/ship/kernel"
)

const (
	approvalTTL = 15 * time.Minute

	helperVerbShip         helperVerb = "ship"
	helperVerbRollback     helperVerb = "rollback"
	helperVerbExec         helperVerb = "exec"
	helperVerbRead         helperVerb = "read"
	helperVerbSecretSet    helperVerb = "secret_set"
	helperVerbSecretRead   helperVerb = "secret_read"
	helperVerbSecretRemove helperVerb = "secret_remove"
	helperVerbPreviewPin   helperVerb = "preview_pin"
	helperVerbShare        helperVerb = "share"
	helperVerbData         helperVerb = "data"
	helperVerbDataSave     helperVerb = "data_save"
	helperVerbRemoveEnv    helperVerb = "rm"
	helperVerbMember       helperVerb = "member"
	helperVerbBoxMutation  helperVerb = "box_mutation"

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
	helperVerbShare: {
		store.MemberRoleShipper: roleScopeAny,
	},
	helperVerbData: {
		store.MemberRoleShipper: roleScopeAny,
	},
	helperVerbDataSave: {
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
	serverAuthorizedMember  *serverMember
	approvalNow             = func() time.Time { return time.Now().UTC() }
)

func setServerMemberFingerprint(fingerprint string) {
	serverMemberFingerprint = strings.TrimSpace(fingerprint)
	serverAuthorizedMember = nil
}

func authorizeHelper(verb helperVerb, target authTarget) (serverMember, error) {
	return authorizeHelperWithPolicy(verb, helperRequiredRole(verb, target.Class), target, func(member serverMember) bool {
		return helperAllows(member.Role, verb, target.Class)
	}, true)
}

func authorizeBoxConfigMutation(key store.BoxConfigKey, target authTarget) (serverMember, error) {
	return authorizeHelperWithPolicy(helperVerbBoxMutation, key.WriteRole, target, func(member serverMember) bool {
		return member.Role == store.MemberRoleOwner || member.Role == key.WriteRole
	}, key.OutOfRoleNeedsApproval)
}

func authorizeHelperWithPolicy(verb helperVerb, requiredRole store.MemberRole, target authTarget, allowed func(serverMember) bool, mintApproval bool) (serverMember, error) {
	member, err := resolveServerMember()
	if err != nil {
		return serverMember{}, err
	}
	serverAuthorizedMember = &member
	if allowed(member) {
		return member, nil
	}
	if !mintApproval {
		return serverMember{}, errcat.New(errcat.CodeRoleDenied, errcat.Fields{
			"member":  member.Name,
			"role":    string(member.Role),
			"summary": target.Summary,
			"command": "ask an owner to run " + target.Summary,
		})
	}
	if consumed, err := consumeApprovedRequest(member, verb, target); err != nil {
		return serverMember{}, err
	} else if consumed {
		return member, nil
	}
	request, minted, err := requestApproval(member, requiredRole, verb, target)
	if err != nil {
		return serverMember{}, err
	}
	if minted {
		recordApprovalJournalEntry("requested", request, member)
		webhookApprovalRequested(request, approvalNow())
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
	file, err := store.Default().ReadApprovals()
	if err != nil {
		return serverMember{}, err
	}
	for _, request := range file.Requests {
		if request.ID == id {
			if err := authorizeApprovalGrantForRequest(member, request); err != nil {
				return serverMember{}, err
			}
			return member, nil
		}
	}
	if member.Role == store.MemberRoleAgent {
		return serverMember{}, errcat.New(errcat.CodeRoleDenied, errcat.Fields{
			"member":  member.Name,
			"role":    string(member.Role),
			"summary": "approve " + id,
			"command": "ask an owner or shipper to run " + approvalCommand(id),
		})
	}
	return member, nil
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

func helperRequiredRole(verb helperVerb, class string) store.MemberRole {
	if helperAllows(store.MemberRoleShipper, verb, class) {
		return store.MemberRoleShipper
	}
	return store.MemberRoleOwner
}

func resolveServerMember() (serverMember, error) {
	fingerprint := strings.TrimSpace(serverMemberFingerprint)
	if fingerprint == "" && os.Getenv("SUDO_USER") == "" {
		return serverMember{Name: "root", Role: store.MemberRoleOwner}, nil
	}
	if fingerprint == "" {
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": "(missing)", "box": boxClientAddress()})
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
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": fingerprint, "box": boxClientAddress()})
	}
	members, err := store.Default().ReadMembers()
	if err != nil {
		return serverMember{}, err
	}
	records := memberkeys.EffectiveMemberRecords(keys, *members, nil)
	record, ok := records[fingerprint]
	if !ok {
		return serverMember{}, errcat.New(errcat.CodeMemberUnknown, errcat.Fields{"fingerprint": fingerprint, "box": boxClientAddress()})
	}
	return serverMember{
		Fingerprint: fingerprint,
		Name:        record.Name,
		Role:        record.Role,
	}, nil
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

func authTargetForAppEnv(app, env, summaryVerb string, args ...string) authTarget {
	class := envClassForAuth(app, env)
	return authTarget{
		App:     app,
		Env:     env,
		Class:   class,
		Args:    append([]string(nil), args...),
		Summary: summarizeTarget(summaryVerb, app, env, class, args...),
	}
}

func authTargetForPreviewBranch(app, branch, summaryVerb string, args ...string) authTarget {
	summaryArgs := append([]string{"branch=" + branch}, args...)
	return authTarget{
		App:     app,
		Class:   "preview",
		Args:    summaryArgs,
		Summary: summarizeTarget(summaryVerb, app, "", "preview", summaryArgs...),
	}
}

func authTargetForBox(summary string, args ...string) authTarget {
	return authTarget{
		Args:    append([]string(nil), args...),
		Summary: summary,
	}
}

func summarizeTarget(summaryVerb, app, env, class string, args ...string) string {
	parts := []string{}
	if summaryVerb = strings.TrimSpace(summaryVerb); summaryVerb != "" {
		parts = append(parts, summaryVerb)
	}
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

func requestApproval(member serverMember, requiredRole store.MemberRole, verb helperVerb, target authTarget) (store.ApprovalRequest, bool, error) {
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
		RequiredRole: requiredRole,
		Verb:         string(verb),
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
				"box":     boxClientAddress(),
			})
		}
		file.Requests = append(file.Requests[:i], file.Requests[i+1:]...)
		if err := store.Default().WriteApprovals(*file); err != nil {
			return false, err
		}
		recordApprovalJournalEntry("consumed", request, member)
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
				"box":     boxClientAddress(),
			})
		}
		if request.Status == store.ApprovalStatusApproved {
			return store.ApprovalRequest{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
				"detail":  "approval " + id + " is already approved",
				"command": "ship box approval ls " + boxClientAddress(),
			})
		}
		if err := authorizeApprovalGrantForRequest(approver, request); err != nil {
			return store.ApprovalRequest{}, err
		}
		request.Status = store.ApprovalStatusApproved
		request.ApprovedAt = now.Format(time.RFC3339Nano)
		request.ExpiresAt = now.Add(approvalTTL).Format(time.RFC3339Nano)
		request.ApprovedBy = &store.ApprovalMember{
			Fingerprint: approver.Fingerprint,
			Name:        approver.Name,
			Role:        approver.Role,
		}
		file.Requests[i] = request
		if err := store.Default().WriteApprovals(*file); err != nil {
			return store.ApprovalRequest{}, err
		}
		recordApprovalJournalEntry("approved", request, approver)
		return request, nil
	}
	pruned := pruneExpiredApprovals(file, now)
	if pruned {
		_ = store.Default().WriteApprovals(*file)
	}
	return store.ApprovalRequest{}, errcat.New(errcat.CodeOperationFailed, errcat.Fields{
		"detail":  "approval " + id + " was not found",
		"command": "ship box approval ls " + boxClientAddress(),
	})
}

func authorizeApprovalGrantForRequest(approver serverMember, request store.ApprovalRequest) error {
	if approver.Fingerprint == request.Member.Fingerprint {
		eligibleRole := "owner or shipper"
		if request.RequiredRole == store.MemberRoleOwner {
			eligibleRole = "owner"
		}
		return errcat.New(errcat.CodeRoleDenied, errcat.Fields{
			"member":  approver.Name,
			"role":    string(approver.Role),
			"summary": "self-approve request " + request.ID + "; requests cannot be self-approved; ask another " + eligibleRole,
			"command": "ask another " + eligibleRole + " to run " + approvalCommand(request.ID),
		})
	}
	if approvalRoleCovers(approver.Role, request.RequiredRole) {
		return nil
	}
	return errcat.New(errcat.CodeRoleDenied, errcat.Fields{
		"member":  approver.Name,
		"role":    string(approver.Role),
		"summary": "approve request " + request.ID + "; request requires " + string(request.RequiredRole),
		"command": "ask an owner to run " + approvalCommand(request.ID),
	})
}

func approvalRoleCovers(approverRole, requiredRole store.MemberRole) bool {
	if approverRole == store.MemberRoleOwner {
		return requiredRole == store.MemberRoleOwner || requiredRole == store.MemberRoleShipper
	}
	return approverRole == store.MemberRoleShipper && requiredRole == store.MemberRoleShipper
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
		"box":     boxClientAddress(),
	})
}

func approvalCommand(id string) string {
	return "ship box approval grant " + id + " " + boxClientAddress()
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

func appendApprovalJournalEntry(event string, request store.ApprovalRequest, actor serverMember) error {
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
	path := store.Default().ApprovalsJournalPath()
	return kernel.AppendJournal(path, entry)
}

func recordApprovalJournalEntry(event string, request store.ApprovalRequest, actor serverMember) {
	if err := appendApprovalJournalEntry(event, request, actor); err != nil {
		fmt.Fprintf(os.Stderr, "warning: approval mutation succeeded but failed to write approval journal %s: %v\n", store.Default().ApprovalsJournalPath(), err)
	}
}

func currentServerMemberForJournal() *activationrecords.Member {
	if serverAuthorizedMember == nil {
		return nil
	}
	return &activationrecords.Member{
		Fingerprint: serverAuthorizedMember.Fingerprint,
		Name:        serverAuthorizedMember.Name,
		Role:        string(serverAuthorizedMember.Role),
	}
}

func utilsDieError(err error) {
	utils.DieError(err, 1)
}
