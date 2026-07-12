package hostinstall

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
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

	"github.com/fprl/ship/internal/errcat"
	"github.com/fprl/ship/internal/version"
)

const (
	defaultReleaseBaseURL = "https://github.com/fprl/ship/releases/download"
	defaultReleaseAPIURL  = "https://api.github.com/repos/fprl/ship"
)

var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc[0-9]+)?$`)

// PrepareHelperBinaryForArch resolves the same client-matched Linux helper
// used by box setup. Day-N update deliberately shares setup's release,
// prebuilt-binary, and local-build behavior.
func (i *Installer) PrepareHelperBinaryForArch(target, arch string) (string, func(), error) {
	return i.prepareRemoteHelperBinary(Plan{TargetHost: target}, arch)
}

func (i *Installer) prepareRemoteHelperBinary(plan Plan, arch string) (string, func(), error) {
	name := "ship-linux-" + arch
	if helper, ok, err := i.localHelperBinary(plan, name, arch); err != nil {
		return "", func() {}, err
	} else if ok {
		return helper, func() {}, nil
	}

	if isReleaseVersion(version.Version) {
		return i.downloadReleaseHelperBinary(plan, version.Version, name)
	}

	if repoRoot, err := locateRepoRoot(); err == nil {
		helperDir, cleanup, err := i.prepareGoHelperBinaries(repoRoot, plan.TargetHost)
		if err != nil {
			return "", cleanup, err
		}
		helper := filepath.Join(helperDir, name)
		if fileExists(helper) {
			return helper, cleanup, nil
		}
		cleanup()
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "ship helper binary not found for target architecture " + arch + ": " + helper,
			"command": helperBuildCommand(arch),
		})
	}

	return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
		"detail":  "ship Linux helper binary " + name + " is required for remote install",
		"command": "SHIP_REPO_ROOT=<path-to-ship-checkout> " + boxSetupCommand(plan.TargetHost),
	})
}

func (i *Installer) localHelperBinary(plan Plan, name, arch string) (string, bool, error) {
	if exact := strings.TrimSpace(i.Env["SHIP_LINUX_HELPER"]); exact != "" {
		if fileExists(exact) {
			if err := validateLinuxHelperBinary(exact, arch); err != nil {
				return "", false, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
					"detail":  "SHIP_LINUX_HELPER " + err.Error(),
					"command": "SHIP_LINUX_HELPER=<path-to-" + name + "> " + boxSetupCommand(plan.TargetHost),
				})
			}
			i.info("Using ship Linux helper binary from %s", exact)
			return exact, true, nil
		}
		return "", false, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "SHIP_LINUX_HELPER does not point at an existing helper binary: " + exact,
			"command": "SHIP_LINUX_HELPER=<path-to-" + name + "> " + boxSetupCommand(plan.TargetHost),
		})
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

func validateLinuxHelperBinary(path, arch string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot be read: %v", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("is not a regular file: %s", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("is empty: %s", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot be opened: %v", err)
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil || !bytes.Equal(magic, []byte{0x7f, 'E', 'L', 'F'}) {
		return fmt.Errorf("is not an ELF binary: %s", path)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("cannot read ELF header: %v", err)
	}
	file, err := elf.NewFile(f)
	if err != nil {
		return fmt.Errorf("has an invalid ELF header: %s", path)
	}
	defer file.Close()

	want, ok := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}[arch]
	if !ok {
		return fmt.Errorf("has unsupported target architecture %q", arch)
	}
	if file.Machine != want {
		return fmt.Errorf("has ELF machine %s, need %s for target architecture %s", file.Machine, want, arch)
	}
	return nil
}

func (i *Installer) downloadReleaseHelperBinary(plan Plan, tag string, name string) (string, func(), error) {
	baseURL := strings.TrimRight(envDefault(i.Env, "SHIP_RELEASE_BASE_URL", defaultReleaseBaseURL), "/")
	downloadURL := baseURL + "/" + tag + "/" + name
	i.info("Downloading ship Linux helper binary from %s", downloadURL)

	client := http.Client{Timeout: 2 * time.Minute}
	token := releaseDownloadToken(i.Env)
	remediation := helperDownloadCommand(plan.TargetHost, baseURL, token)
	data, err := i.downloadReleaseAsset(&client, tag, name, token, downloadURL, baseURL, remediation)
	if err != nil {
		return "", func() {}, err
	}

	sumsURL := baseURL + "/" + tag + "/SHA256SUMS"
	sums, err := i.downloadReleaseAsset(&client, tag, "SHA256SUMS", token, sumsURL, baseURL, remediation)
	if err != nil {
		return "", func() {}, err
	}
	if err := verifyReleaseAssetChecksum(name, data, sums, remediation); err != nil {
		return "", func() {}, err
	}

	return writeExecutableTempFile(name, bytes.NewReader(data))
}

func (i *Installer) downloadReleaseAsset(client *http.Client, tag string, name string, token string, downloadURL string, baseURL string, remediation string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, helperDownloadError("build helper download request failed: "+oneLineError(err), remediation)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, helperDownloadError("download "+downloadURL+" failed: "+oneLineError(err), remediation)
	}
	if resp.StatusCode != http.StatusOK {
		if token != "" && resp.StatusCode == http.StatusNotFound && canUseReleaseAPI(i.Env, baseURL) {
			_ = resp.Body.Close()
			return i.downloadGitHubReleaseAsset(client, tag, name, token, remediation)
		}
		_ = resp.Body.Close()
		return nil, helperDownloadError("download "+downloadURL+" failed: HTTP "+resp.Status, remediation)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, helperDownloadError("read "+downloadURL+" failed: "+oneLineError(err), remediation)
	}
	return data, nil
}

func (i *Installer) downloadGitHubReleaseAsset(client *http.Client, tag string, name string, token string, remediation string) ([]byte, error) {
	apiBaseURL := strings.TrimRight(envDefault(i.Env, "SHIP_RELEASE_API_BASE_URL", defaultReleaseAPIURL), "/")
	releaseURL := apiBaseURL + "/releases/tags/" + url.PathEscape(tag)

	req, err := http.NewRequest(http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, helperDownloadError("build GitHub release request failed: "+oneLineError(err), remediation)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, helperDownloadError("download "+name+" via GitHub API failed: "+oneLineError(err), remediation)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, helperDownloadError("download "+name+" via GitHub API failed: HTTP "+resp.Status, remediation)
	}

	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, helperDownloadError("decode GitHub release response failed: "+oneLineError(err), remediation)
	}

	var assetURL string
	for _, asset := range release.Assets {
		if asset.Name == name {
			assetURL = asset.URL
			break
		}
	}
	if assetURL == "" {
		return nil, helperDownloadError("release "+tag+" does not contain asset "+name, remediation)
	}

	assetReq, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, helperDownloadError("build GitHub asset request failed: "+oneLineError(err), remediation)
	}
	assetReq.Header.Set("Authorization", "Bearer "+token)
	assetReq.Header.Set("Accept", "application/octet-stream")

	assetResp, err := client.Do(assetReq)
	if err != nil {
		return nil, helperDownloadError("download "+name+" via GitHub API failed: "+oneLineError(err), remediation)
	}
	defer assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK {
		return nil, helperDownloadError("download "+name+" via GitHub API failed: HTTP "+assetResp.Status, remediation)
	}

	data, err := io.ReadAll(assetResp.Body)
	if err != nil {
		return nil, helperDownloadError("read "+name+" via GitHub API failed: "+oneLineError(err), remediation)
	}
	return data, nil
}

func verifyReleaseAssetChecksum(name string, data []byte, sums []byte, remediation string) error {
	want, err := checksumForAsset(name, sums, remediation)
	if err != nil {
		return err
	}
	gotBytes := sha256.Sum256(data)
	got := hex.EncodeToString(gotBytes[:])
	if !strings.EqualFold(got, want) {
		return helperDownloadError("checksum mismatch for "+name+": got "+got+", want "+want, remediation)
	}
	return nil
}

func checksumForAsset(name string, sums []byte, remediation string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == name || strings.TrimPrefix(fields[1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != sha256.Size*2 {
				return "", helperDownloadError("invalid SHA256SUMS entry for "+name, remediation)
			}
			return fields[0], nil
		}
	}
	return "", helperDownloadError("SHA256SUMS does not contain "+name, remediation)
}

func writeExecutableTempFile(name string, reader io.Reader) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ship-helper-")
	if err != nil {
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "create temporary helper file dir failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp ship box setup <ssh-target>",
		})
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, name)
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		cleanup()
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "create temporary helper file failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp ship box setup <ssh-target>",
		})
	}
	if _, err := io.Copy(out, reader); err != nil {
		_ = out.Close()
		cleanup()
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "write temporary helper file failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp ship box setup <ssh-target>",
		})
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", func() {}, errcat.New(errcat.CodeHostHelperUnavailable, errcat.Fields{
			"detail":  "close temporary helper file failed: " + oneLineError(err),
			"command": "TMPDIR=/tmp ship box setup <ssh-target>",
		})
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
