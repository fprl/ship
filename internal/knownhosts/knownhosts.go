package knownhosts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DisplayPath                = "~/.config/ship/known_hosts"
	SetupHostKeyChangedMessage = "host key changed since last setup — re-pinning (box rebuilt?)"
)

type entry struct {
	raw         string
	hosts       []string
	keyMaterial string
}

func Path() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "ship", "known_hosts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ship", "known_hosts"), nil
}

func Ensure() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func TempFile() (string, func(), error) {
	dir, err := os.MkdirTemp("", "ship-known-hosts-")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func SSHOptions(path, strict string) []string {
	return []string{
		"-o", "UserKnownHostsFile=" + path,
		"-o", "StrictHostKeyChecking=" + strict,
		"-o", "HashKnownHosts=no",
	}
}

func CanonicalSSHOptions(strict string) ([]string, error) {
	path, err := Ensure()
	if err != nil {
		return nil, err
	}
	return SSHOptions(path, strict), nil
}

func ListHosts() ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		parsed, ok := parse(line)
		if !ok {
			continue
		}
		for _, host := range parsed.hosts {
			host = displayHost(host)
			if host == "" || strings.HasPrefix(host, "|") || seen[host] {
				continue
			}
			seen[host] = true
			out = append(out, host)
		}
	}
	return out, nil
}

func KnownBoxesCause(hosts []string) string {
	var b strings.Builder
	b.WriteString("known boxes (")
	b.WriteString(DisplayPath)
	b.WriteString("):")
	if len(hosts) == 0 {
		b.WriteString("\n  none known yet")
		return b.String()
	}
	for _, host := range hosts {
		b.WriteString("\n  ")
		b.WriteString(host)
	}
	return b.String()
}

func Remove(host string) (bool, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return false, nil
	}
	path, err := Path()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	next, removed := removeHostLines(string(data), host)
	if !removed {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(next), 0600); err != nil {
		return false, err
	}
	return true, os.Chmod(path, 0600)
}

func Reconcile(host, tempPath string) (bool, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return false, nil
	}
	tempData, err := os.ReadFile(tempPath)
	if err != nil {
		return false, err
	}
	newEntries := matchingEntries(string(tempData), host)
	if len(newEntries) == 0 {
		return false, fmt.Errorf("setup known_hosts did not contain %s", host)
	}

	path, err := Ensure()
	if err != nil {
		return false, err
	}
	currentData, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	oldEntries := matchingEntries(string(currentData), host)
	changed := len(oldEntries) > 0 && !sameHostKeys(oldEntries, newEntries)

	next, _ := removeHostLines(string(currentData), host)
	next = strings.TrimRight(next, "\n")
	if next != "" {
		next += "\n"
	}
	for _, item := range newEntries {
		next += strings.TrimSpace(item.raw) + "\n"
	}
	if err := os.WriteFile(path, []byte(next), 0600); err != nil {
		return false, err
	}
	return changed, os.Chmod(path, 0600)
}

func matchingEntries(data, host string) []entry {
	var out []entry
	for _, line := range strings.Split(data, "\n") {
		parsed, ok := parse(line)
		if !ok {
			continue
		}
		if parsed.matches(host) {
			out = append(out, parsed)
		}
	}
	return out
}

func removeHostLines(data, host string) (string, bool) {
	var lines []string
	removed := false
	for _, line := range strings.Split(data, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parsed, ok := parse(line)
		if ok && parsed.matches(host) {
			removed = true
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", removed
	}
	return strings.Join(lines, "\n") + "\n", removed
}

func parse(line string) (entry, bool) {
	raw := strings.TrimSpace(line)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return entry{}, false
	}
	fields := strings.Fields(raw)
	hostIndex := 0
	if strings.HasPrefix(fields[0], "@") {
		hostIndex = 1
	}
	if len(fields) <= hostIndex+2 {
		return entry{}, false
	}
	hosts := strings.Split(fields[hostIndex], ",")
	return entry{
		raw:         raw,
		hosts:       hosts,
		keyMaterial: fields[hostIndex+1] + " " + fields[hostIndex+2],
	}, true
}

func (e entry) matches(host string) bool {
	for _, candidate := range e.hosts {
		if hostMatches(candidate, host) {
			return true
		}
	}
	return false
}

func hostMatches(candidate, host string) bool {
	candidate = strings.TrimSpace(candidate)
	host = strings.TrimSpace(host)
	return candidate == host || displayHost(candidate) == host
}

func displayHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end > 1 {
			return host[1:end]
		}
	}
	return host
}

func sameHostKeys(a, b []entry) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, item := range a {
		counts[item.keyMaterial]++
	}
	for _, item := range b {
		counts[item.keyMaterial]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
