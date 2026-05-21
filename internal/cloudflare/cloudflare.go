package cloudflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/store"
)

var ErrNotConfigured = errors.New("Cloudflare integration is not configured")

func CloudflareApiTokenPath() string {
	if p := os.Getenv("SIMPLE_VPS_CLOUDFLARE_API_TOKEN_PATH"); p != "" {
		return p
	}
	return "/etc/simple-vps/cloudflare-api-token"
}

func CloudflaredTunnelTokenPath() string {
	if p := os.Getenv("SIMPLE_VPS_CLOUDFLARED_TUNNEL_TOKEN_PATH"); p != "" {
		return p
	}
	return "/etc/cloudflared/tunnel-token"
}

type IngressItem struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

type TunnelConfig struct {
	Ingress []IngressItem `json:"ingress"`
}

type CloudflareResponse struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Client helper

func ReadCloudflareApiToken(path string) (string, error) {
	if path == "" {
		path = CloudflareApiTokenPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func CloudflareApiRequest(token string, method string, apiPath string, payload interface{}, query url.Values) (json.RawMessage, error) {
	u := "https://api.cloudflare.com/client/v4" + apiPath
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var cfResp CloudflareResponse
	if err := json.Unmarshal(respBody, &cfResp); err != nil {
		return nil, fmt.Errorf("Cloudflare API returned invalid JSON: %w (status %d)", err, resp.StatusCode)
	}

	if !cfResp.Success {
		var msgs []string
		for _, e := range cfResp.Errors {
			msgs = append(msgs, e.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, string(respBody))
		}
		return nil, fmt.Errorf("Cloudflare API failed (%d): %s", resp.StatusCode, strings.Join(msgs, "; "))
	}

	return cfResp.Result, nil
}

func CloudflareAccountId(token string, preferred string) (string, error) {
	if preferred != "" {
		return preferred, nil
	}
	q := url.Values{}
	q.Set("per_page", "50")
	res, err := CloudflareApiRequest(token, "GET", "/accounts", nil, q)
	if err != nil {
		return "", err
	}
	var accounts []struct {
		Id   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(res, &accounts); err != nil {
		return "", fmt.Errorf("invalid accounts response: %w", err)
	}
	if len(accounts) == 0 {
		return "", errors.New("no Cloudflare accounts found")
	}
	if len(accounts) != 1 {
		return "", errors.New("Cloudflare account id is required when the API token can access multiple accounts")
	}
	return accounts[0].Id, nil
}

func EnsureCloudflareTunnel(token string, accountId string, name string) (string, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	res, err := CloudflareApiRequest(token, "GET", fmt.Sprintf("/accounts/%s/cfd_tunnel", accountId), nil, q)
	if err != nil {
		return "", err
	}
	var tunnels []struct {
		Id        string `json:"id"`
		Name      string `json:"name"`
		DeletedAt string `json:"deleted_at"`
	}
	if err := json.Unmarshal(res, &tunnels); err == nil {
		for _, t := range tunnels {
			if t.Name == name && t.DeletedAt == "" {
				return t.Id, nil
			}
		}
	}

	// Create tunnel
	payload := map[string]string{"name": name, "config_src": "cloudflare"}
	createdRes, err := CloudflareApiRequest(token, "POST", fmt.Sprintf("/accounts/%s/cfd_tunnel", accountId), payload, nil)
	if err != nil {
		return "", err
	}
	var tunnel struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(createdRes, &tunnel); err != nil {
		return "", fmt.Errorf("invalid create tunnel response: %w", err)
	}
	return tunnel.Id, nil
}

func CloudflareTunnelConfig(token string, accountId string, tunnelId string) (*TunnelConfig, error) {
	res, err := CloudflareApiRequest(token, "GET", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountId, tunnelId), nil, nil)
	if err != nil {
		return nil, err
	}
	var configWrap struct {
		Config TunnelConfig `json:"config"`
	}
	if err := json.Unmarshal(res, &configWrap); err != nil {
		return nil, fmt.Errorf("invalid configuration response: %w", err)
	}
	if len(configWrap.Config.Ingress) == 0 {
		configWrap.Config.Ingress = []IngressItem{{Service: "http_status:404"}}
	}
	return &configWrap.Config, nil
}

func PutCloudflareTunnelConfig(token string, accountId string, tunnelId string, config *TunnelConfig) error {
	payload := map[string]interface{}{"config": config}
	_, err := CloudflareApiRequest(token, "PUT", fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountId, tunnelId), payload, nil)
	return err
}

func WithCloudflareHostname(config *TunnelConfig, host string, service string) *TunnelConfig {
	var nextIngress []IngressItem
	var catchAll []IngressItem

	for _, item := range config.Ingress {
		if item.Hostname == "" {
			catchAll = append(catchAll, item)
		} else if item.Hostname != host {
			nextIngress = append(nextIngress, item)
		}
	}
	nextIngress = append(nextIngress, IngressItem{Hostname: host, Service: service})
	if len(catchAll) == 0 {
		catchAll = []IngressItem{{Service: "http_status:404"}}
	}
	return &TunnelConfig{Ingress: append(nextIngress, catchAll...)}
}

func WithoutCloudflareHostname(config *TunnelConfig, host string) *TunnelConfig {
	var nextIngress []IngressItem
	hasCatchAll := false

	for _, item := range config.Ingress {
		if item.Hostname != host {
			nextIngress = append(nextIngress, item)
			if item.Hostname == "" {
				hasCatchAll = true
			}
		}
	}
	if !hasCatchAll {
		nextIngress = append(nextIngress, IngressItem{Service: "http_status:404"})
	}
	return &TunnelConfig{Ingress: nextIngress}
}

func CloudflareZoneForHost(token string, host string) (string, error) {
	parts := strings.Split(host, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		q := url.Values{}
		q.Set("name", candidate)
		q.Set("per_page", "1")
		res, err := CloudflareApiRequest(token, "GET", "/zones", nil, q)
		if err != nil {
			continue
		}
		var zones []struct {
			Id string `json:"id"`
		}
		if err := json.Unmarshal(res, &zones); err == nil && len(zones) > 0 {
			return zones[0].Id, nil
		}
	}
	return "", fmt.Errorf("Cloudflare zone not found for %s", host)
}

func EnsureCloudflareCname(token string, zoneId string, host string, target string) (string, error) {
	q := url.Values{}
	q.Set("type", "CNAME")
	q.Set("name", host)
	q.Set("per_page", "100")
	res, err := CloudflareApiRequest(token, "GET", fmt.Sprintf("/zones/%s/dns_records", zoneId), nil, q)
	if err != nil {
		return "", err
	}
	var records []struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(res, &records); err != nil {
		return "", fmt.Errorf("invalid DNS records response: %w", err)
	}

	payload := map[string]interface{}{
		"type":    "CNAME",
		"name":    host,
		"content": target,
		"ttl":     1,
		"proxied": true,
	}

	if len(records) > 1 {
		return "", fmt.Errorf("multiple Cloudflare CNAME records found for %s", host)
	}

	if len(records) == 1 {
		recordId := records[0].Id
		_, err := CloudflareApiRequest(token, "PATCH", fmt.Sprintf("/zones/%s/dns_records/%s", zoneId, recordId), payload, nil)
		if err != nil {
			return "", err
		}
		return recordId, nil
	}

	createdRes, err := CloudflareApiRequest(token, "POST", fmt.Sprintf("/zones/%s/dns_records", zoneId), payload, nil)
	if err != nil {
		return "", err
	}
	var record struct {
		Id string `json:"id"`
	}
	if err := json.Unmarshal(createdRes, &record); err != nil {
		return "", fmt.Errorf("invalid DNS record creation response: %w", err)
	}
	return record.Id, nil
}

func DeleteCloudflareDnsRecord(token string, zoneId string, recordId string) error {
	_, err := CloudflareApiRequest(token, "DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", zoneId, recordId), nil, nil)
	return err
}

func ConfiguredCloudflare() (string, *store.CloudflareFile, string, string, error) {
	stateObj, err := store.Default().ReadCloudflare()
	if err != nil {
		return "", nil, "", "", err
	}
	token, err := ReadCloudflareApiToken("")
	if err != nil {
		return "", nil, "", "", err
	}
	if token == "" || stateObj.TunnelID == "" || stateObj.AccountID == "" {
		return "", nil, "", "", ErrNotConfigured
	}
	return token, stateObj, stateObj.AccountID, stateObj.TunnelID, nil
}

type CloudflareIngress struct {
	Token     string
	State     *store.CloudflareFile
	AccountId string
	TunnelId  string
}

func NewCloudflareIngress() (*CloudflareIngress, error) {
	token, cfState, accountId, tunnelId, err := ConfiguredCloudflare()
	if err != nil {
		return nil, err
	}
	return &CloudflareIngress{
		Token:     token,
		State:     cfState,
		AccountId: accountId,
		TunnelId:  tunnelId,
	}, nil
}

func (c *CloudflareIngress) ReplaceTunnelConfig(prevConfig *TunnelConfig, nextConfig *TunnelConfig, afterReplace func() (string, string, error)) (string, string, error) {
	if err := PutCloudflareTunnelConfig(c.Token, c.AccountId, c.TunnelId, nextConfig); err != nil {
		return "", "", err
	}
	zoneId, recId, err := afterReplace()
	if err != nil {
		// Rollback tunnel config
		_ = PutCloudflareTunnelConfig(c.Token, c.AccountId, c.TunnelId, prevConfig)
		return "", "", err
	}
	return zoneId, recId, nil
}

func (c *CloudflareIngress) Publish(host string, app string) (string, error) {
	prevConfig, err := CloudflareTunnelConfig(c.Token, c.AccountId, c.TunnelId)
	if err != nil {
		return "", err
	}
	nextConfig := WithCloudflareHostname(prevConfig, host, "http://127.0.0.1:8080")

	zoneId, recordId, err := c.ReplaceTunnelConfig(prevConfig, nextConfig, func() (string, string, error) {
		zoneId, err := CloudflareZoneForHost(c.Token, host)
		if err != nil {
			return "", "", err
		}
		recordId, err := EnsureCloudflareCname(c.Token, zoneId, host, c.TunnelId+".cfargotunnel.com")
		if err != nil {
			return "", "", err
		}
		return zoneId, recordId, nil
	})
	if err != nil {
		return "", err
	}

	if c.State.Routes == nil {
		c.State.Routes = make(map[string]store.CloudflareRoute)
	}
	c.State.Routes[host] = store.CloudflareRoute{
		App:         app,
		ZoneID:      zoneId,
		DNSRecordID: recordId,
	}

	stateStore := store.Default()
	if err := stateStore.WriteCloudflare(*c.State); err != nil {
		return "", err
	}
	return fmt.Sprintf("Cloudflare route ready: %s", host), nil
}

func (c *CloudflareIngress) RemoveHost(host string, routeState store.CloudflareRoute) error {
	prevConfig, err := CloudflareTunnelConfig(c.Token, c.AccountId, c.TunnelId)
	if err != nil {
		return err
	}
	nextConfig := WithoutCloudflareHostname(prevConfig, host)

	_, _, err = c.ReplaceTunnelConfig(prevConfig, nextConfig, func() (string, string, error) {
		if routeState.ZoneID != "" && routeState.DNSRecordID != "" {
			err := DeleteCloudflareDnsRecord(c.Token, routeState.ZoneID, routeState.DNSRecordID)
			if err != nil {
				return "", "", err
			}
		}
		return "", "", nil
	})
	return err
}

func (c *CloudflareIngress) Remove(host string, app string) ([]string, error) {
	if c.State.Routes == nil {
		c.State.Routes = make(map[string]store.CloudflareRoute)
	}

	var targets []struct {
		host string
		rc   store.CloudflareRoute
	}

	if host != "" {
		if rc, ok := c.State.Routes[host]; ok {
			targets = append(targets, struct {
				host string
				rc   store.CloudflareRoute
			}{host, rc})
		}
	} else if app != "" {
		normApp, err := store.NormalizeApp(app)
		if err != nil {
			return nil, err
		}
		for h, rc := range c.State.Routes {
			if rc.App == normApp {
				targets = append(targets, struct {
					host string
					rc   store.CloudflareRoute
				}{h, rc})
			}
		}
	}

	if len(targets) == 0 {
		return nil, nil
	}

	var removed []string
	for _, target := range targets {
		if err := c.RemoveHost(target.host, target.rc); err != nil {
			return nil, err
		}
		delete(c.State.Routes, target.host)
		removed = append(removed, target.host)
	}

	stateStore := store.Default()
	if err := stateStore.WriteCloudflare(*c.State); err != nil {
		return nil, err
	}
	return removed, nil
}
