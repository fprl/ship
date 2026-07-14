package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fprl/ship/internal/names"
)

// Exported regexes used outside the package:
//   - AppRe is consumed by callers that validate app names directly.
//   - SystemUserRe is consumed by `internal/host` (the host-side
//     primitives package) for SUDO_USER validation.
var (
	AppRe        = names.AppRe
	SystemUserRe = names.SystemUserRe
)

func newHostFile() *HostFile {
	file := HostFile{Version: CurrentVersion}
	normalizeHostFile(&file)
	return &file
}

func normalizeHostFile(file *HostFile) {
	file.Version = CurrentVersion
	normalizeHostDesired(&file.Desired)
	normalizeHostObserved(&file.Observed)
}

func normalizeHostDesired(desired *HostDesired) {
	if desired.Packages == nil {
		desired.Packages = map[string]DesiredPackage{}
	}
}

func normalizeHostObserved(observed *HostObserved) {
	if observed.Packages == nil {
		observed.Packages = map[string]ObservedPackage{}
	}
}

func normalizeDoctorFile(file *DoctorFile) {
	file.Version = CurrentVersion
	if file.Checks == nil {
		file.Checks = []DoctorCheck{}
	}
	if file.Delta == nil {
		file.Delta = []DoctorCheck{}
	}
}

func normalizeMembersFile(file *MembersFile) {
	file.Version = CurrentVersion
	if file.Members == nil {
		file.Members = map[string]MemberRecord{}
	}
}

func normalizeApprovalsFile(file *ApprovalsFile) {
	file.Version = CurrentVersion
	if file.Requests == nil {
		file.Requests = []ApprovalRequest{}
	}
	for i := range file.Requests {
		if file.Requests[i].Status == "" {
			file.Requests[i].Status = ApprovalStatusPending
		}
	}
}

func ValidMemberRole(role MemberRole) bool {
	switch role {
	case MemberRoleOwner, MemberRoleShipper, MemberRoleAgent:
		return true
	default:
		return false
	}
}

func validateHostDesired(desired HostDesired) error {
	if strings.TrimSpace(desired.Users.Operator) == "" {
		return errors.New("users.operator is required")
	}
	if strings.TrimSpace(desired.Users.Deploy) == "" {
		return errors.New("users.deploy is required")
	}
	switch desired.Ingress.Expose {
	case ExposePublic, ExposePrivate:
	default:
		return errors.New("ingress.expose must be public or private")
	}
	for name, pkg := range desired.Packages {
		if strings.TrimSpace(name) == "" {
			return errors.New("packages cannot contain empty names")
		}
		if strings.TrimSpace(pkg.Source) == "" {
			return fmt.Errorf("packages.%s.source is required", name)
		}
	}
	return nil
}

func validateVersion(scope string, version int) error {
	if version == 0 {
		return fmt.Errorf("%s version is required", scope)
	}
	if version > CurrentVersion {
		return fmt.Errorf("unsupported %s version %d", scope, version)
	}
	return nil
}

func validateMembersFile(file MembersFile) error {
	for fingerprint, member := range file.Members {
		if strings.TrimSpace(fingerprint) == "" {
			return errors.New("members cannot contain empty fingerprints")
		}
		if strings.TrimSpace(member.Name) == "" {
			return fmt.Errorf("members.%s.name is required", fingerprint)
		}
		if !ValidMemberRole(member.Role) {
			return fmt.Errorf("members.%s.role must be owner, shipper, or agent", fingerprint)
		}
	}
	return nil
}

func validateApprovalsFile(file ApprovalsFile) error {
	seen := map[string]bool{}
	for _, request := range file.Requests {
		if strings.TrimSpace(request.ID) == "" {
			return errors.New("approvals.requests.id is required")
		}
		if seen[request.ID] {
			return fmt.Errorf("approvals.requests contains duplicate id %s", request.ID)
		}
		seen[request.ID] = true
		if err := validateApprovalRequest(request); err != nil {
			return err
		}
	}
	return nil
}

// dropInvalidExpiredApprovals removes requests that fail validation AND are
// already past their expiry (or carry an unparseable expiry). Approvals are
// 15-minute objects: an expired entry is prunable garbage whatever its
// shape, and it must never brick reads of the live entries. Invalid entries
// that are still live keep failing validation loudly.
func dropInvalidExpiredApprovals(file *ApprovalsFile, now time.Time) {
	kept := file.Requests[:0]
	for _, request := range file.Requests {
		if validateApprovalRequest(request) != nil {
			expires, err := time.Parse(time.RFC3339Nano, request.ExpiresAt)
			if err != nil || !expires.After(now) {
				continue
			}
		}
		kept = append(kept, request)
	}
	file.Requests = kept
}

func validateApprovalRequest(request ApprovalRequest) error {
	if strings.TrimSpace(request.Member.Fingerprint) == "" {
		return fmt.Errorf("approvals.requests.%s.member.fingerprint is required", request.ID)
	}
	if strings.TrimSpace(request.Member.Name) == "" {
		return fmt.Errorf("approvals.requests.%s.member.name is required", request.ID)
	}
	if !ValidMemberRole(request.Member.Role) {
		return fmt.Errorf("approvals.requests.%s.member.role must be owner, shipper, or agent", request.ID)
	}
	switch request.RequiredRole {
	case MemberRoleOwner, MemberRoleShipper:
	default:
		return fmt.Errorf("approvals.requests.%s.required_role must be owner or shipper", request.ID)
	}
	if strings.TrimSpace(request.Verb) == "" {
		return fmt.Errorf("approvals.requests.%s.verb is required", request.ID)
	}
	if strings.TrimSpace(request.Target.Summary) == "" {
		return fmt.Errorf("approvals.requests.%s.target.summary is required", request.ID)
	}
	if strings.TrimSpace(request.MatchKey) == "" {
		return fmt.Errorf("approvals.requests.%s.match_key is required", request.ID)
	}
	switch request.Status {
	case ApprovalStatusPending, ApprovalStatusApproved:
	default:
		return fmt.Errorf("approvals.requests.%s.status must be pending or approved", request.ID)
	}
	if strings.TrimSpace(request.CreatedAt) == "" {
		return fmt.Errorf("approvals.requests.%s.created is required", request.ID)
	}
	if strings.TrimSpace(request.ExpiresAt) == "" {
		return fmt.Errorf("approvals.requests.%s.expires is required", request.ID)
	}
	if request.Status == ApprovalStatusApproved {
		if strings.TrimSpace(request.ApprovedAt) == "" {
			return fmt.Errorf("approvals.requests.%s.approved_at is required", request.ID)
		}
		if request.ApprovedBy == nil {
			return fmt.Errorf("approvals.requests.%s.approved_by is required", request.ID)
		}
		if !ValidMemberRole(request.ApprovedBy.Role) {
			return fmt.Errorf("approvals.requests.%s.approved_by.role must be owner, shipper, or agent", request.ID)
		}
	}
	return nil
}
