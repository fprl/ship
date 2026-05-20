package cloudflare

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCloudflaredTunnelTokenDefaultPathMatchesServerContract(t *testing.T) {
	t.Setenv("SIMPLE_VPS_CLOUDFLARED_TUNNEL_TOKEN_PATH", "")
	if got := CloudflaredTunnelTokenPath(); got != "/etc/cloudflared/tunnel-token" {
		t.Fatalf("unexpected tunnel token path: %s", got)
	}
}

func TestWithCloudflareHostnameUpsertsBeforeCatchAll(t *testing.T) {
	config := &TunnelConfig{Ingress: []IngressItem{
		{Hostname: "old.example.com", Service: "http://127.0.0.1:8080"},
		{Service: "http_status:404"},
	}}

	next := WithCloudflareHostname(config, "api.example.com", "http://127.0.0.1:8080")

	want := []IngressItem{
		{Hostname: "old.example.com", Service: "http://127.0.0.1:8080"},
		{Hostname: "api.example.com", Service: "http://127.0.0.1:8080"},
		{Service: "http_status:404"},
	}
	if len(next.Ingress) != len(want) {
		t.Fatalf("unexpected ingress length: %+v", next.Ingress)
	}
	for i := range want {
		if next.Ingress[i] != want[i] {
			t.Fatalf("ingress[%d]: want %+v, got %+v", i, want[i], next.Ingress[i])
		}
	}
}

func TestWithCloudflareHostnameReplacesExistingHostname(t *testing.T) {
	config := &TunnelConfig{Ingress: []IngressItem{
		{Hostname: "api.example.com", Service: "http://127.0.0.1:3000"},
		{Service: "http_status:404"},
	}}

	next := WithCloudflareHostname(config, "api.example.com", "http://127.0.0.1:8080")

	if len(next.Ingress) != 2 {
		t.Fatalf("unexpected ingress length: %+v", next.Ingress)
	}
	if next.Ingress[0] != (IngressItem{Hostname: "api.example.com", Service: "http://127.0.0.1:8080"}) {
		t.Fatalf("hostname was not replaced: %+v", next.Ingress[0])
	}
	if next.Ingress[1] != (IngressItem{Service: "http_status:404"}) {
		t.Fatalf("catch-all was not preserved: %+v", next.Ingress[1])
	}
}

func TestWithoutCloudflareHostnameKeepsCatchAll(t *testing.T) {
	config := &TunnelConfig{Ingress: []IngressItem{
		{Hostname: "api.example.com", Service: "http://127.0.0.1:8080"},
		{Hostname: "old.example.com", Service: "http://127.0.0.1:8080"},
	}}

	next := WithoutCloudflareHostname(config, "api.example.com")

	want := []IngressItem{
		{Hostname: "old.example.com", Service: "http://127.0.0.1:8080"},
		{Service: "http_status:404"},
	}
	if len(next.Ingress) != len(want) {
		t.Fatalf("unexpected ingress length: %+v", next.Ingress)
	}
	for i := range want {
		if next.Ingress[i] != want[i] {
			t.Fatalf("ingress[%d]: want %+v, got %+v", i, want[i], next.Ingress[i])
		}
	}
}

func TestConfiguredCloudflareReportsNotConfiguredWithoutTokenOrTunnel(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SIMPLE_VPS_CLOUDFLARE_STATE_PATH", filepath.Join(root, "cloudflare.json"))
	t.Setenv("SIMPLE_VPS_CLOUDFLARE_API_TOKEN_PATH", filepath.Join(root, "token"))

	_, _, _, _, err := ConfiguredCloudflare()
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
