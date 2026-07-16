package store

import (
	"fmt"
	"strings"

	"github.com/fprl/ship/internal/config"
)

const CurrentVersion = 1

type ExposeMode string

const (
	ExposePublic  ExposeMode = "public"
	ExposePrivate ExposeMode = "private"
)

type DoctorCheck struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Remediation string `json:"remediation"`
}

type DoctorFile struct {
	Version    int           `json:"version"`
	RecordedAt string        `json:"recorded_at"`
	Checks     []DoctorCheck `json:"checks"`
}

type MemberRole string

const (
	MemberRoleOwner   MemberRole = "owner"
	MemberRoleShipper MemberRole = "shipper"
	MemberRoleAgent   MemberRole = "agent"
)

type MemberRecord struct {
	Name string     `json:"name"`
	Role MemberRole `json:"role"`
}

type MembersFile struct {
	Version int                     `json:"version"`
	Members map[string]MemberRecord `json:"members"`
}

type BoxConfigValueType string

const (
	BoxConfigValueTypeURLOrEmpty BoxConfigValueType = "url_or_empty"
	BoxConfigValueTypeAddress    BoxConfigValueType = "host"
)

type BoxConfigKey struct {
	Name                   string
	Type                   BoxConfigValueType
	Default                string
	WriteRole              MemberRole
	OutOfRoleNeedsApproval bool
}

// BoxConfigSchema is the complete box-config authority boundary. A generic
// setter is safe only because every key declares its authorization policy.
var BoxConfigSchema = []BoxConfigKey{
	{
		Name:                   "webhook.url",
		Type:                   BoxConfigValueTypeURLOrEmpty,
		Default:                "",
		WriteRole:              MemberRoleOwner,
		OutOfRoleNeedsApproval: true,
	},
	{
		Name:                   "box.address",
		Type:                   BoxConfigValueTypeAddress,
		Default:                "",
		WriteRole:              MemberRoleOwner,
		OutOfRoleNeedsApproval: true,
	},
}

type BoxConfigFile struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

type BoxConfigKeyUnknownError struct {
	Key       string
	ValidKeys []string
}

func (e *BoxConfigKeyUnknownError) Error() string {
	return "unknown box config key: " + e.Key
}

type BoxConfigValueError struct {
	Key    string
	Detail string
}

func (e *BoxConfigValueError) Error() string {
	return "invalid box config value for " + e.Key + ": " + e.Detail
}

func LookupBoxConfigKey(name string) (BoxConfigKey, bool) {
	for _, key := range BoxConfigSchema {
		if key.Name == name {
			return key, true
		}
	}
	return BoxConfigKey{}, false
}

func ValidateBoxConfigValue(key, value string) error {
	spec, ok := LookupBoxConfigKey(key)
	if !ok {
		return &BoxConfigKeyUnknownError{Key: key, ValidKeys: BoxConfigKeys()}
	}
	switch spec.Type {
	case BoxConfigValueTypeURLOrEmpty:
		if value != "" {
			if err := config.ValidateWebhookURL(value); err != nil {
				return &BoxConfigValueError{Key: key, Detail: err.Error()}
			}
		}
	case BoxConfigValueTypeAddress:
		if err := validateBoxAddress(value); err != nil {
			return &BoxConfigValueError{Key: key, Detail: err.Error()}
		}
	default:
		return &BoxConfigValueError{Key: key, Detail: "unsupported value type"}
	}
	return nil
}

func validateBoxAddress(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("must be a host")
	}
	if !config.ValidateHost(value) {
		return fmt.Errorf("host is invalid")
	}
	return nil
}

func ValidateBoxConfigFile(file BoxConfigFile) error {
	for key, value := range file.Values {
		if err := ValidateBoxConfigValue(key, value); err != nil {
			return err
		}
	}
	return nil
}

func BoxConfigKeys() []string {
	keys := make([]string, 0, len(BoxConfigSchema))
	for _, key := range BoxConfigSchema {
		keys = append(keys, key.Name)
	}
	return keys
}

type ApprovalMember struct {
	Fingerprint string     `json:"fingerprint"`
	Name        string     `json:"name"`
	Role        MemberRole `json:"role"`
}

type ApprovalTarget struct {
	App     string   `json:"app,omitempty"`
	Env     string   `json:"env,omitempty"`
	Class   string   `json:"class,omitempty"`
	Args    []string `json:"args,omitempty"`
	Summary string   `json:"summary"`
}

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
)

type ApprovalRequest struct {
	ID           string          `json:"id"`
	Member       ApprovalMember  `json:"member"`
	RequiredRole MemberRole      `json:"required_role"`
	Verb         string          `json:"verb"`
	Target       ApprovalTarget  `json:"target"`
	MatchKey     string          `json:"match_key"`
	Status       ApprovalStatus  `json:"status"`
	CreatedAt    string          `json:"created"`
	ExpiresAt    string          `json:"expires"`
	ApprovedAt   string          `json:"approved_at,omitempty"`
	ApprovedBy   *ApprovalMember `json:"approved_by,omitempty"`
}

type ApprovalsFile struct {
	Version  int               `json:"version"`
	Requests []ApprovalRequest `json:"requests"`
}
