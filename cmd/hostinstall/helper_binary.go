package hostinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fprl/simple-vps/internal/version"
)

const (
	defaultReleaseBaseURL = "https://github.com/fprl/simple-vps/releases/download"
	defaultReleaseAPIURL  = "https://api.github.com/repos/fprl/simple-vps"
)

var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$`)

func (i *Installer) prepareRemoteHelperBinary(arch string) (string, func(), error) {
	name := "ship-linux-" + arch
	if helper, ok, err := i.localHelperBinary(name); err != nil {
		return "", func() {}, err
	} else if ok {
		return helper, func() {}, nil
	}

	if isReleaseVersion(version.Version) {
		return i.downloadReleaseHelperBinary(version.Version, name)
	}

	if repoRoot, err := locateRepoRoot(); err == nil {
		helperDir, cleanup, err := i.prepareGoHelperBinaries(repoRoot)
		if err != nil {
			return "", cleanup, err
		}
		helper := filepath.Join(helperDir, name)
		if fileExists(helper) {
			return helper, cleanup, nil
		}
		cleanup()
		return "", func() {}, fmt.Errorf("ship helper binary not found for target architecture %s: %s", arch, helper)
	}

	return "", func() {}, fmt.Errorf("ship Linux helper binary %q is required for remote install. Run from a checkout, place %q beside this binary, set SHIP_HELPER_DIR, or use a tagged release build that can download the matching helper asset", name, name)
}

func (i *Installer) localHelperBinary(name string) (string, bool, error) {
	if exact := strings.TrimSpace(i.Env["SHIP_LINUX_HELPER"]); exact != "" {
		if fileExists(exact) {
			i.info("Using ship Linux helper binary from %s", exact)
			return exact, true, nil
		}
		return "", false, fmt.Errorf("SHIP_LINUX_HELPER does not exist or is not executable: %s", exact)
	}

	var candidates []string
	if dir := strings.TrimSpace(i.Env["SHIP_HELPER_DIR"]); dir != "" {
		candidates = append(candidates, filepath.Join(dir, name))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, name), filepath.Join(cwd, "dist", name))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, name), filepath.Join(exeDir, "dist", name))
	}

	for _, candidate := range candidates {
		if fileExists(candidate) {
			i.info("Using ship Linux helper binary from %s", candidate)
			return candidate, true, nil
		}
	}
	return "", false, nil
}

func (i *Installer) downloadReleaseHelperBinary(tag string, name string) (string, func(), error) {
	baseURL := strings.TrimRight(envDefault(i.Env, "SHIP_RELEASE_BASE_URL", defaultReleaseBaseURL), "/")
	downloadURL := baseURL + "/" + tag + "/" + name
	i.info("Downloading ship Linux helper binary from %s", downloadURL)

	client := http.Client{Timeout: 2 * time.Minute}
	token := releaseDownloadToken(i.Env)
	data, err := i.downloadReleaseAsset(&client, tag, name, token, downloadURL, baseURL)
	if err != nil {
		return "", func() {}, err
	}

	sumsURL := baseURL + "/" + tag + "/SHA256SUMS"
	sums, err := i.downloadReleaseAsset(&client, tag, "SHA256SUMS", token, sumsURL, baseURL)
	if err != nil {
		return "", func() {}, err
	}
	if err := verifyReleaseAssetChecksum(name, data, sums); err != nil {
		return "", func() {}, err
	}

	return writeExecutableTempFile(name, bytes.NewReader(data))
}

func (i *Installer) downloadReleaseAsset(client *http.Client, tag string, name string, token string, downloadURL string, baseURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if token != "" && resp.StatusCode == http.StatusNotFound && canUseReleaseAPI(i.Env, baseURL) {
			_ = resp.Body.Close()
			return i.downloadGitHubReleaseAsset(client, tag, name, token)
		}
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s failed: HTTP %d", downloadURL, resp.StatusCode)
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (i *Installer) downloadGitHubReleaseAsset(client *http.Client, tag string, name string, token string) ([]byte, error) {
	apiBaseURL := strings.TrimRight(envDefault(i.Env, "SHIP_RELEASE_API_BASE_URL", defaultReleaseAPIURL), "/")
	releaseURL := apiBaseURL + "/releases/tags/" + url.PathEscape(tag)

	req, err := http.NewRequest(http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s via GitHub API failed: HTTP %d", name, resp.StatusCode)
	}

	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
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
		return nil, err
	}
	assetReq.Header.Set("Authorization", "Bearer "+token)
	assetReq.Header.Set("Accept", "application/octet-stream")

	assetResp, err := client.Do(assetReq)
	if err != nil {
		return nil, err
	}
	defer assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s via GitHub API failed: HTTP %d", name, assetResp.StatusCode)
	}

	return io.ReadAll(assetResp.Body)
}

func verifyReleaseAssetChecksum(name string, data []byte, sums []byte) error {
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

func writeExecutableTempFile(name string, reader io.Reader) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ship-helper-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, name)
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func canUseReleaseAPI(env map[string]string, baseURL string) bool {
	return strings.TrimSpace(env["SHIP_RELEASE_API_BASE_URL"]) != "" || baseURL == defaultReleaseBaseURL
}

func releaseDownloadToken(env map[string]string) string {
	for _, key := range []string{"SHIP_RELEASE_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(env[key]); token != "" {
			return token
		}
	}
	return ""
}

func isReleaseVersion(value string) bool {
	return releaseVersionPattern.MatchString(value)
}
