package cloudflare

import (
	"testing"
)

func TestCloudflaredTunnelTokenDefaultPathMatchesServerContract(t *testing.T) {
	t.Setenv("SHIP_CLOUDFLARED_TUNNEL_TOKEN_PATH", "")
	if got := CloudflaredTunnelTokenPath(); got != "/etc/cloudflared/tunnel-token" {
		t.Fatalf("unexpected tunnel token path: %s", got)
	}
}
