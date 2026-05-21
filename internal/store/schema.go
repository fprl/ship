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
	Docker     bool     `json:"docker"`
	Litestream bool     `json:"litestream"`
	Runtimes   []string `json:"runtimes"`
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
	InstalledAt      string     `json:"installed_at,omitempty"`
	SimpleVPSVersion string     `json:"simple_vps_version,omitempty"`
	LastApply        *ApplyMeta `json:"last_apply,omitempty"`
}

type ApplyMeta struct {
	ID                string `json:"id"`
	StartedAt         string `json:"started_at"`
	FinishedAt        string `json:"finished_at"`
	Status            string `json:"status"`
	OperationsChanged int    `json:"operations_changed"`
}

type AppsFile struct {
	Version int            `json:"version"`
	Apps    map[string]App `json:"apps"`
}

type App struct {
	Path           string   `json:"path"`
	Services       []string `json:"services"`
	CurrentRelease string   `json:"current_release"`
}

type RoutesFile struct {
	Version int     `json:"version"`
	Routes  []Route `json:"routes"`
}

type Route struct {
	Host    string            `json:"host"`
	Type    string            `json:"type"`
	App     string            `json:"app,omitempty"`
	Service string            `json:"service,omitempty"`
	Port    *int              `json:"port,omitempty"`
	Root    string            `json:"root,omitempty"`
	To      string            `json:"to,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
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
