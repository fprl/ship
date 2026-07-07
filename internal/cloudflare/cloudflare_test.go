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

func TestCloudflareApiTokenDefaultPathMatchesServerContract(t *testing.T) {
	t.Setenv("SHIP_CLOUDFLARE_API_TOKEN_PATH", "")
	if got := CloudflareApiTokenPath(); got != "/etc/ship/cloudflare-api-token" {
		t.Fatalf("unexpected api token path: %s", got)
	}
}
