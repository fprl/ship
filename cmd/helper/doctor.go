package helper

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/store"
)

var (
	BroadSudoRe  = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\((?:ALL|ALL:ALL)\)\s+NOPASSWD:\s*ALL$`)
	HelperSudoRe = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\(root\)\s+NOPASSWD:\s*/usr/local/bin/simple-vps\s+server\s+app\s+\*,\s*/usr/local/bin/simple-vps\s+server\s+status,\s*/usr/local/bin/simple-vps\s+server\s+status\s+\*,\s*/usr/local/bin/simple-vps\s+server\s+doctor,\s*/usr/local/bin/simple-vps\s+server\s+doctor\s+\*$`)
)

type doctorCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of the text summary."`
}

func (c doctorCmd) Run() error {
	CmdDoctor(c.JSON)
	return nil
}

func SudoersDir() string {
	if p := os.Getenv("SIMPLE_VPS_SUDOERS_DIR"); p != "" {
		return p
	}
	return "/etc/sudoers.d"
}

func sudoersPaths() []string {
	dir := SudoersDir()
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, f := range files {
		if !f.IsDir() {
			paths = append(paths, filepath.Join(dir, f.Name()))
		}
	}
	sort.Strings(paths)
	return paths
}

func sudoersLines(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

func sudoersUsersMatching(path string, pattern *regexp.Regexp) map[string]bool {
	users := make(map[string]bool)
	for _, line := range sudoersLines(path) {
		m := pattern.FindStringSubmatch(line)
		if m != nil {
			users[m[1]] = true
		}
	}
	return users
}

func allSudoersUsersMatching(pattern *regexp.Regexp) map[string]bool {
	users := make(map[string]bool)
	for _, p := range sudoersPaths() {
		for u := range sudoersUsersMatching(p, pattern) {
			users[u] = true
		}
	}
	return users
}

func doctorIdentityFindings() []string {
	dir := SudoersDir()
	operatorFile := filepath.Join(dir, "operator")
	helperFile := filepath.Join(dir, "simple-vps")

	broadUsers := allSudoersUsersMatching(BroadSudoRe)

	operatorUsersMap := sudoersUsersMatching(operatorFile, BroadSudoRe)
	deployUsersMap := sudoersUsersMatching(helperFile, HelperSudoRe)

	var operatorUsers []string
	for u := range operatorUsersMap {
		operatorUsers = append(operatorUsers, u)
	}
	sort.Strings(operatorUsers)

	var deployUsers []string
	for u := range deployUsersMap {
		deployUsers = append(deployUsers, u)
	}
	sort.Strings(deployUsers)

	var findings []string

	if len(operatorUsers) == 0 {
		findings = append(findings, fmt.Sprintf("missing broad operator sudoers grant in %s", operatorFile))
	}
	if len(operatorUsers) > 1 {
		findings = append(findings, fmt.Sprintf("multiple operator sudoers users in %s: %s", operatorFile, strings.Join(operatorUsers, ", ")))
	}

	if len(deployUsers) == 0 {
		findings = append(findings, fmt.Sprintf("missing deploy helper sudoers grant in %s", helperFile))
	}
	if len(deployUsers) > 1 {
		findings = append(findings, fmt.Sprintf("multiple deploy sudoers users in %s: %s", helperFile, strings.Join(deployUsers, ", ")))
	}

	if len(operatorUsers) > 0 && len(deployUsers) > 0 {
		operatorUser := operatorUsers[0]
		deployUser := deployUsers[0]
		if operatorUser == deployUser {
			findings = append(findings, fmt.Sprintf("operator and deploy are both %s", operatorUser))
		}
		if broadUsers[deployUser] {
			findings = append(findings, fmt.Sprintf("deploy user %s has broad passwordless sudo", deployUser))
		}
	}

	return findings
}

func CmdDoctor(jsonFlag bool) {
	stateStore := store.Default()
	stateFindings := doctorStateFindings(stateStore)
	serviceFindings := doctorServiceFindings(stateStore, host.SystemServiceStatus)
	identityFindings := doctorIdentityFindings()
	report := doctorReportFor(stateFindings, serviceFindings, identityFindings)

	if jsonFlag {
		buf, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(buf))
	} else {
		fmt.Print(renderDoctorText(report))
	}
	if !report.Healthy {
		os.Exit(1)
	}
}

type doctorReport struct {
	State    doctorSection `json:"state"`
	Services doctorSection `json:"services"`
	Identity doctorSection `json:"identity"`
	Healthy  bool          `json:"healthy"`
}

type doctorSection struct {
	Status   string   `json:"status"`
	Findings []string `json:"findings"`
}

func doctorReportFor(stateFindings []string, serviceFindings []string, identityFindings []string) doctorReport {
	report := doctorReport{
		State:    doctorSectionFor(stateFindings),
		Services: doctorSectionFor(serviceFindings),
		Identity: doctorSectionFor(identityFindings),
	}
	report.Healthy = report.State.Status == "healthy" &&
		report.Services.Status == "healthy" &&
		report.Identity.Status == "healthy"
	return report
}

func doctorSectionFor(findings []string) doctorSection {
	status := "healthy"
	if len(findings) > 0 {
		status = "degraded"
	}
	if findings == nil {
		findings = []string{}
	}
	return doctorSection{Status: status, Findings: findings}
}

func renderDoctorText(report doctorReport) string {
	var b strings.Builder
	b.WriteString("Simple VPS doctor\n")
	writeDoctorSectionText(&b, "state", report.State)
	writeDoctorSectionText(&b, "services", report.Services)
	writeDoctorSectionText(&b, "identity", report.Identity)
	return b.String()
}

func writeDoctorSectionText(b *strings.Builder, name string, section doctorSection) {
	fmt.Fprintf(b, "%s: %s\n", name, section.Status)
	for _, f := range section.Findings {
		fmt.Fprintf(b, "  - %s\n", f)
	}
}

func doctorStateFindings(stateStore store.Store) []string {
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return []string{fmt.Sprintf("cannot read host install state: %v", err)}
	}
	if !installed {
		return []string{fmt.Sprintf("host is not installed (missing %s)", stateStore.HostPath())}
	}

	var findings []string
	if _, err := stateStore.ReadHost(); err != nil {
		findings = append(findings, fmt.Sprintf("host state: %v", err))
	}
	// ReadCloudflare stays because Cloudflare Tunnel provider state is
	// host-level: set by the installer, used at routing time.
	if _, err := stateStore.ReadCloudflare(); err != nil {
		findings = append(findings, fmt.Sprintf("cloudflare state: %v", err))
	}
	return findings
}

func doctorServiceFindings(stateStore store.Store, serviceStatus func(string) string) []string {
	installed, err := stateStore.HostInstalled()
	if err != nil || !installed {
		return nil
	}
	hostFile, err := stateStore.ReadHost()
	if err != nil {
		return nil
	}
	return doctorServiceFindingsFor(hostFile.Desired, serviceStatus)
}

func doctorServiceFindingsFor(desired store.HostDesired, serviceStatus func(string) string) []string {
	required := []string{"caddy"}
	switch desired.Ingress.Tunnel {
	case store.TunnelCloudflare:
		required = append(required, "cloudflared")
	case store.TunnelTailscaleFunnel:
		required = append(required, "tailscaled")
	}

	var findings []string
	for _, service := range required {
		status := serviceStatus(service)
		if status != "active" {
			findings = append(findings, fmt.Sprintf("%s service is %s (expected active)", service, status))
		}
	}
	return findings
}
