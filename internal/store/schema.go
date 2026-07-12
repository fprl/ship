package store

const CurrentVersion = 1

type ExposeMode string

const (
	ExposePublic  ExposeMode = "public"
	ExposePrivate ExposeMode = "private"
)

type TunnelMode string

const (
	TunnelNone            TunnelMode = "none"
	TunnelCloudflare      TunnelMode = "cloudflare"
	TunnelTailscaleFunnel TunnelMode = "tailscale-funnel"
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
	Tunnel TunnelMode `json:"tunnel"`
}

type HostFeatures struct {
	Docker     bool `json:"docker"`
	Litestream bool `json:"litestream"`
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
	UFW80443Allowed          bool `json:"ufw_80_443_allowed"`
	CloudflaredServiceActive bool `json:"cloudflared_service_active"`
}

type HostMeta struct {
	InstalledAt string     `json:"installed_at,omitempty"`
	ShipVersion string     `json:"ship_version,omitempty"`
	LastApply   *ApplyMeta `json:"last_apply,omitempty"`
}

type ApplyMeta struct {
	ID                string `json:"id"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	Status            string `json:"status"`
	OperationsChanged int    `json:"operations_changed"`
}

type CloudflareRoute struct {
	App         string `json:"app"`
	ZoneID      string `json:"zone_id"`
	DNSRecordID string `json:"dns_record_id"`
}

type CloudflareFile struct {
	Version    int                        `json:"version"`
	AccountID  string                     `json:"account_id,omitempty"`
	TunnelID   string                     `json:"tunnel_id,omitempty"`
	TunnelName string                     `json:"tunnel_name,omitempty"`
	Routes     map[string]CloudflareRoute `json:"routes"`
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

// BoxNotifyFile is box-global pager configuration. It deliberately lives
// beside members and approvals: no app owns box-level notifications.
type BoxNotifyFile struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
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
	ID         string          `json:"id"`
	Member     ApprovalMember  `json:"member"`
	Verb       string          `json:"verb"`
	Target     ApprovalTarget  `json:"target"`
	MatchKey   string          `json:"match_key"`
	Status     ApprovalStatus  `json:"status"`
	CreatedAt  string          `json:"created"`
	ExpiresAt  string          `json:"expires"`
	ApprovedAt string          `json:"approved_at,omitempty"`
	ApprovedBy *ApprovalMember `json:"approved_by,omitempty"`
}

type ApprovalsFile struct {
	Version  int               `json:"version"`
	Requests []ApprovalRequest `json:"requests"`
}
