package store

import "github.com/fprl/ship/internal/config"

const CurrentVersion = 1

type ExposeMode string

const (
	ExposePublic  ExposeMode = "public"
	ExposePrivate ExposeMode = "private"
)

type HostFile struct {
	Version  int          `json:"version"`
	Desired  HostDesired  `json:"desired"`
	Observed HostObserved `json:"observed"`
	Meta     HostMeta     `json:"meta"`
}

type HostDesired struct {
	Users    HostUsers                 `json:"users"`
	Ingress  HostIngressDesired        `json:"ingress"`
	Features HostFeatures              `json:"features"`
	Packages map[string]DesiredPackage `json:"packages"`
}

type HostUsers struct {
	Operator string `json:"operator"`
	Deploy   string `json:"deploy"`
}

type HostIngressDesired struct {
	Expose ExposeMode `json:"expose"`
}

type HostFeatures struct {
	Docker bool `json:"docker"`
}

type DesiredPackage struct {
	Source  string `json:"source"`
	Track   string `json:"track,omitempty"`
	Version string `json:"version,omitempty"`
}

type HostObserved struct {
	Packages map[string]ObservedPackage `json:"packages"`
	Ingress  HostIngressObserved        `json:"ingress"`
}

type ObservedPackage struct {
	Version string `json:"version"`
}

type HostIngressObserved struct {
	UFW80443Allowed bool `json:"ufw_80_443_allowed"`
}

type HostMeta struct {
	InstalledAt       string     `json:"installed_at,omitempty"`
	ShipVersion       string     `json:"ship_version,omitempty"`
	LastClientVersion string     `json:"last_client_version,omitempty"`
	ClientAddress     string     `json:"client_address,omitempty"`
	LastApply         *ApplyMeta `json:"last_apply,omitempty"`
}

type ApplyMeta struct {
	ID                string `json:"id"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	Status            string `json:"status"`
	OperationsChanged int    `json:"operations_changed"`
}

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
	Delta      []DoctorCheck `json:"delta"`
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
	default:
		return &BoxConfigValueError{Key: key, Detail: "unsupported value type"}
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
