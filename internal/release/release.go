// Package release downloads and verifies ship release artifacts.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://github.com/fprl/ship/releases/download"
	defaultAPIURL  = "https://api.github.com/repos/fprl/ship"
)

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$`)

// IsVersion reports whether value names a published ship release.
func IsVersion(value string) bool {
	return versionPattern.MatchString(value)
}

// DownloadVerifiedAsset fetches an artifact and its SHA256SUMS entry before
// returning any bytes to its caller. Env supports the release base URL, token,
// and GitHub API fallback used by setup.
func DownloadVerifiedAsset(env map[string]string, tag, name string) ([]byte, error) {
	baseURL := strings.TrimRight(envDefault(env, "SHIP_RELEASE_BASE_URL", DefaultBaseURL), "/")
	client := http.Client{Timeout: 2 * time.Minute}
	token := downloadToken(env)

	data, err := downloadAsset(&client, env, tag, name, token, baseURL+"/"+tag+"/"+name, baseURL)
	if err != nil {
		return nil, err
	}
	sums, err := downloadAsset(&client, env, tag, "SHA256SUMS", token, baseURL+"/"+tag+"/SHA256SUMS", baseURL)
	if err != nil {
		return nil, err
	}
	if err := VerifyAssetChecksum(name, data, sums); err != nil {
		return nil, err
	}
	return data, nil
}

func downloadAsset(client *http.Client, env map[string]string, tag, name, token, downloadURL, baseURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build release download request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", downloadURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if token != "" && resp.StatusCode == http.StatusNotFound && canUseAPI(env, baseURL) {
			return downloadGitHubAsset(client, env, tag, name, token)
		}
		return nil, fmt.Errorf("download %s: HTTP %s", downloadURL, resp.Status)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", downloadURL, err)
	}
	return data, nil
}

func downloadGitHubAsset(client *http.Client, env map[string]string, tag, name, token string) ([]byte, error) {
	apiBaseURL := strings.TrimRight(envDefault(env, "SHIP_RELEASE_API_BASE_URL", defaultAPIURL), "/")
	req, err := http.NewRequest(http.MethodGet, apiBaseURL+"/releases/tags/"+url.PathEscape(tag), nil)
	if err != nil {
		return nil, fmt.Errorf("build GitHub release request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s via GitHub API: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s via GitHub API: HTTP %s", name, resp.Status)
	}
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode GitHub release response: %w", err)
	}
	var assetURL string
	for _, asset := range release.Assets {
		if asset.Name == name {
			assetURL = asset.URL
			break
		}
	}
	if assetURL == "" {
		return nil, fmt.Errorf("release %s does not contain asset %s", tag, name)
	}
	assetReq, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build GitHub asset request: %w", err)
	}
	assetReq.Header.Set("Authorization", "Bearer "+token)
	assetReq.Header.Set("Accept", "application/octet-stream")
	assetResp, err := client.Do(assetReq)
	if err != nil {
		return nil, fmt.Errorf("download %s via GitHub API: %w", name, err)
	}
	defer assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s via GitHub API: HTTP %s", name, assetResp.Status)
	}
	data, err := io.ReadAll(assetResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s via GitHub API: %w", name, err)
	}
	return data, nil
}

// VerifyAssetChecksum verifies data against name's entry in SHA256SUMS.
func VerifyAssetChecksum(name string, data, sums []byte) error {
	want, err := checksumForAsset(name, sums)
	if err != nil {
		return err
	}
	gotBytes := sha256.Sum256(data)
	got := hex.EncodeToString(gotBytes[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

func checksumForAsset(name string, sums []byte) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == name || strings.TrimPrefix(fields[1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("invalid SHA256SUMS entry for %s", name)
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS does not contain %s", name)
}

func canUseAPI(env map[string]string, baseURL string) bool {
	return strings.TrimSpace(env["SHIP_RELEASE_API_BASE_URL"]) != "" || baseURL == DefaultBaseURL
}

func downloadToken(env map[string]string) string {
	for _, key := range []string{"SHIP_RELEASE_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(env[key]); token != "" {
			return token
		}
	}
	return ""
}

func envDefault(env map[string]string, name, fallback string) string {
	if value := strings.TrimSpace(env[name]); value != "" {
		return value
	}
	return fallback
}
