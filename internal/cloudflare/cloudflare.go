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
)

func CloudflareApiTokenPath() string {
	if p := os.Getenv("SHIP_CLOUDFLARE_API_TOKEN_PATH"); p != "" {
		return p
	}
	return "/etc/ship/cloudflare-api-token"
}

func CloudflaredTunnelTokenPath() string {
	if p := os.Getenv("SHIP_CLOUDFLARED_TUNNEL_TOKEN_PATH"); p != "" {
		return p
	}
	return "/etc/cloudflared/tunnel-token"
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
