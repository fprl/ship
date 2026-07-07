package helper

import (
	"bufio"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	doctorStatusOK       = "ok"
	doctorStatusDegraded = "degraded"
	doctorStatusFailed   = "failed"

	doctorCheckHostState      = "host_state"
	doctorCheckServiceHealth  = "service_health"
	doctorCheckSudoersID      = "sudoers_identity"
	doctorCheckDiskSpace      = "disk_space"
	doctorCheckTLSCerts       = "tls_certs"
	doctorCheckReaperTimer    = "reaper_timer"
	doctorCheckDeployJournals = "deploy_journals"

	reaperTimerUnit = "ship-preview-reaper.timer"
)

var (
	BroadSudoRe  = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\((?:ALL|ALL:ALL)\)\s+NOPASSWD:\s*ALL$`)
	HelperSudoRe = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\(root\)\s+NOPASSWD:\s*/usr/local/bin/ship\s+server\s+app\s+\*,\s*/usr/local/bin/ship\s+server\s+status,\s*/usr/local/bin/ship\s+server\s+status\s+\*,\s*/usr/local/bin/ship\s+server\s+doctor,\s*/usr/local/bin/ship\s+server\s+doctor\s+\*$`)
)

type doctorCmd struct {
	JSON      bool   `name:"json" help:"Emit structured JSON instead of the text summary."`
	BoxTarget string `name:"box-target" hidden:"" help:"SSH target used to render runnable remediation commands."`
	Action    string `arg:"" optional:"" help:"Optional action. record persists doctor state for the daily timer."`
}

func (c doctorCmd) Run() error {
	if c.Action == "record" {
		CmdDoctorRecord()
		return nil
	}
	if c.Action != "" {
		return fmt.Errorf("unsupported doctor action: %s", c.Action)
	}
	CmdDoctor(c.JSON, c.BoxTarget)
	return nil
}

func SudoersDir() string {
	if p := os.Getenv("SHIP_SUDOERS_DIR"); p != "" {
		return p
	}
	return "/etc/sudoers.d"
}

func systemdUnitDir() string {
	if p := os.Getenv("SHIP_SYSTEMD_UNIT_DIR"); p != "" {
		return p
	}
	return "/etc/systemd/system"
}

func caddyDataDir() string {
	if p := os.Getenv("SHIP_CADDY_DATA_DIR"); p != "" {
		return p
	}
	return "/var/lib/caddy"
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
	helperFile := filepath.Join(dir, "ship")

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

type doctorOptions struct {
	StateStore  store.Store
	BoxTarget   string
	Now         func() time.Time
	Service     func(string) string
	Timer       func(string) systemdUnitState
	Disk        func(string) (diskUsage, error)
	TLSStatuses func(time.Time) ([]tlsCertStatus, error)
	AppEnvs     func() ([]appEnvStatus, error)
}

func defaultDoctorOptions(boxTarget string) doctorOptions {
	return doctorOptions{
		StateStore:  store.Default(),
		BoxTarget:   boxTarget,
		Now:         time.Now,
		Service:     host.SystemServiceStatus,
		Timer:       systemdTimerState,
		Disk:        diskUsageForPath,
		TLSStatuses: routedTLSCertStatuses,
		AppEnvs:     identityAppEnvs,
	}
}

func normalizeDoctorOptions(opts doctorOptions) doctorOptions {
	if opts.StateStore.Root == "" {
		opts.StateStore = store.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Service == nil {
		opts.Service = host.SystemServiceStatus
	}
	if opts.Timer == nil {
		opts.Timer = systemdTimerState
	}
	if opts.Disk == nil {
		opts.Disk = diskUsageForPath
	}
	if opts.TLSStatuses == nil {
		opts.TLSStatuses = routedTLSCertStatuses
	}
	if opts.AppEnvs == nil {
		opts.AppEnvs = identityAppEnvs
	}
	return opts
}

func CmdDoctor(jsonFlag bool, boxTarget string) {
	checks := doctorChecksFor(defaultDoctorOptions(boxTarget))

	if jsonFlag {
		buf, err := json.MarshalIndent(checks, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(buf))
	} else {
		fmt.Print(renderDoctorText(checks))
	}
	if !doctorChecksOK(checks) {
		os.Exit(1)
	}
}

func CmdDoctorRecord() {
	if _, err := recordDoctorRun(defaultDoctorOptions("")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func recordDoctorRun(opts doctorOptions) (store.DoctorFile, error) {
	opts = normalizeDoctorOptions(opts)
	now := opts.Now().UTC()
	checks := doctorChecksFor(opts)
	previous, err := opts.StateStore.ReadDoctor()
	var previousChecks []store.DoctorCheck
	if err == nil {
		previousChecks = previous.Checks
	} else if !os.IsNotExist(err) {
		previousChecks = nil
	}
	delta := doctorDelta(previousChecks, checks)
	if len(delta) > 0 {
		// TODO(§7): fire notify webhook for newly degraded/failed doctor checks.
	}
	file := store.DoctorFile{
		Version:    store.CurrentVersion,
		RecordedAt: now.Format(time.RFC3339Nano),
		Checks:     checks,
		Delta:      delta,
	}
	if err := opts.StateStore.WriteDoctor(file); err != nil {
		return store.DoctorFile{}, err
	}
	return file, nil
}

func doctorChecksFor(opts doctorOptions) []store.DoctorCheck {
	opts = normalizeDoctorOptions(opts)
	return []store.DoctorCheck{
		doctorHostStateCheck(opts.StateStore, opts.BoxTarget),
		doctorServiceHealthCheck(opts.StateStore, opts.Service, opts.BoxTarget),
		doctorSudoersIdentityCheck(opts.BoxTarget),
		doctorDiskSpaceCheck(opts.Disk, opts.BoxTarget),
		doctorTLSCertsCheck(opts.TLSStatuses, opts.Now(), opts.BoxTarget),
		doctorReaperTimerCheck(opts.Timer, opts.BoxTarget),
		doctorDeployJournalsCheck(opts.AppEnvs, opts.BoxTarget),
	}
}

func doctorChecksOK(checks []store.DoctorCheck) bool {
	for _, check := range checks {
		if check.Status != doctorStatusOK {
			return false
		}
	}
	return true
}

func renderDoctorText(checks []store.DoctorCheck) string {
	var b strings.Builder
	wroteNext := false
	for _, check := range checks {
		fmt.Fprintf(&b, "%s %s - %s", check.ID, check.Status, check.Evidence)
		if !wroteNext && check.Status != doctorStatusOK {
			fmt.Fprintf(&b, "; next: %s", check.Remediation)
			wroteNext = true
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func doctorHostStateCheck(stateStore store.Store, boxTarget string) store.DoctorCheck {
	remediation := doctorBoxInitCommand(boxTarget)
	installed, err := stateStore.HostInstalled()
	if err != nil {
		return doctorCheck(doctorCheckHostState, doctorStatusFailed, fmt.Sprintf("cannot read host install state: %v", err), remediation)
	}
	if !installed {
		return doctorCheck(doctorCheckHostState, doctorStatusFailed, fmt.Sprintf("host is not installed (missing %s)", stateStore.HostPath()), remediation)
	}
	if _, err := stateStore.ReadHost(); err != nil {
		return doctorCheck(doctorCheckHostState, doctorStatusFailed, fmt.Sprintf("host state invalid: %v", err), remediation)
	}
	if _, err := stateStore.ReadCloudflare(); err != nil {
		return doctorCheck(doctorCheckHostState, doctorStatusFailed, fmt.Sprintf("cloudflare state invalid: %v", err), remediation)
	}
	return doctorCheck(doctorCheckHostState, doctorStatusOK, fmt.Sprintf("host state readable (%s)", stateStore.HostPath()), doctorRerunCommand(boxTarget))
}

func doctorServiceHealthCheck(stateStore store.Store, serviceStatus func(string) string, boxTarget string) store.DoctorCheck {
	installed, err := stateStore.HostInstalled()
	if err != nil || !installed {
		return doctorCheck(doctorCheckServiceHealth, doctorStatusDegraded, "host state unavailable; required services cannot be determined", doctorBoxInitCommand(boxTarget))
	}
	hostFile, err := stateStore.ReadHost()
	if err != nil {
		return doctorCheck(doctorCheckServiceHealth, doctorStatusDegraded, "host state invalid; required services cannot be determined", doctorBoxInitCommand(boxTarget))
	}
	findings := doctorServiceFindingsFor(hostFile.Desired, serviceStatus)
	if len(findings) > 0 {
		return doctorCheck(doctorCheckServiceHealth, doctorStatusFailed, strings.Join(findings, "; "), doctorRestartServicesCommand(boxTarget, hostFile.Desired))
	}
	required := requiredServicesFor(hostFile.Desired)
	return doctorCheck(doctorCheckServiceHealth, doctorStatusOK, fmt.Sprintf("%s active", strings.Join(required, ", ")), doctorRerunCommand(boxTarget))
}

func doctorSudoersIdentityCheck(boxTarget string) store.DoctorCheck {
	findings := doctorIdentityFindings()
	if len(findings) > 0 {
		return doctorCheck(doctorCheckSudoersID, doctorStatusDegraded, strings.Join(findings, "; "), doctorBoxInitCommand(boxTarget))
	}
	return doctorCheck(doctorCheckSudoersID, doctorStatusOK, "operator and deploy sudoers grants are split", doctorRerunCommand(boxTarget))
}

type diskUsage struct {
	Path           string
	TotalBytes     uint64
	AvailableBytes uint64
}

func (u diskUsage) usedBytes() uint64 {
	if u.TotalBytes < u.AvailableBytes {
		return 0
	}
	return u.TotalBytes - u.AvailableBytes
}

func (u diskUsage) usedPercent() float64 {
	if u.TotalBytes == 0 {
		return 0
	}
	return float64(u.usedBytes()) / float64(u.TotalBytes) * 100
}

func doctorDiskSpaceCheck(usageForPath func(string) (diskUsage, error), boxTarget string) store.DoctorCheck {
	usage, err := usageForPath("/")
	if err != nil {
		return doctorCheck(doctorCheckDiskSpace, doctorStatusFailed, fmt.Sprintf("cannot read disk usage for /: %v", err), doctorDiskCleanupCommand(boxTarget))
	}
	percent := usage.usedPercent()
	evidence := fmt.Sprintf("%s: used=%.1f%% (%s of %s), available=%s", usage.Path, percent, formatBytes(usage.usedBytes()), formatBytes(usage.TotalBytes), formatBytes(usage.AvailableBytes))
	switch {
	case percent >= 90:
		return doctorCheck(doctorCheckDiskSpace, doctorStatusFailed, evidence, doctorDiskCleanupCommand(boxTarget))
	case percent >= 80:
		return doctorCheck(doctorCheckDiskSpace, doctorStatusDegraded, evidence, doctorDiskCleanupCommand(boxTarget))
	default:
		return doctorCheck(doctorCheckDiskSpace, doctorStatusOK, evidence, doctorRerunCommand(boxTarget))
	}
}

func diskUsageForPath(path string) (diskUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskUsage{}, err
	}
	return diskUsage{
		Path:           path,
		TotalBytes:     stat.Blocks * uint64(stat.Bsize),
		AvailableBytes: stat.Bavail * uint64(stat.Bsize),
	}, nil
}

type tlsCertStatus struct {
	Host     string
	NotAfter time.Time
	Path     string
	Found    bool
}

func doctorTLSCertsCheck(statusesFor func(time.Time) ([]tlsCertStatus, error), now time.Time, boxTarget string) store.DoctorCheck {
	statuses, err := statusesFor(now)
	if err != nil {
		return doctorCheck(doctorCheckTLSCerts, doctorStatusFailed, singleLine(fmt.Sprintf("cannot inspect routed TLS certificates: %v", err)), doctorCaddyRestartCommand(boxTarget))
	}
	if len(statuses) == 0 {
		return doctorCheck(doctorCheckTLSCerts, doctorStatusOK, "0 routed hosts with automatic TLS", doctorRerunCommand(boxTarget))
	}

	highest := doctorStatusOK
	var evidence []string
	for _, status := range statuses {
		if !status.Found {
			highest = worseDoctorStatus(highest, doctorStatusFailed)
			evidence = append(evidence, fmt.Sprintf("%s: certificate not found", status.Host))
			continue
		}
		days := int(math.Floor(status.NotAfter.Sub(now).Hours() / 24))
		switch {
		case days < 0:
			highest = worseDoctorStatus(highest, doctorStatusFailed)
			evidence = append(evidence, fmt.Sprintf("%s: expired %dd ago", status.Host, -days))
		case days < 14:
			highest = worseDoctorStatus(highest, doctorStatusDegraded)
			evidence = append(evidence, fmt.Sprintf("%s: expires in %dd", status.Host, days))
		default:
			evidence = append(evidence, fmt.Sprintf("%s: expires in %dd", status.Host, days))
		}
	}
	return doctorCheck(doctorCheckTLSCerts, highest, strings.Join(evidence, "; "), doctorCaddyRestartCommand(boxTarget))
}

func routedTLSCertStatuses(now time.Time) ([]tlsCertStatus, error) {
	apps, err := identityAppEnvs()
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{}
	for _, app := range apps {
		manifest, err := config.ReadManifest(identity.EnvRoot(app.App, app.Env))
		if err != nil {
			return nil, fmt.Errorf("%s/%s applied manifest: %v", app.App, app.Env, err)
		}
		for _, route := range manifest.Routes {
			if route.Host == "" || normalizeTLS(route.TLS) == "internal" {
				continue
			}
			hosts[route.Host] = true
		}
	}

	var sortedHosts []string
	for host := range hosts {
		sortedHosts = append(sortedHosts, host)
	}
	sort.Strings(sortedHosts)

	statuses := make([]tlsCertStatus, 0, len(sortedHosts))
	for _, host := range sortedHosts {
		status := tlsCertStatus{Host: host}
		cert, path, err := readCaddyCertificate(host)
		if err == nil {
			status.Found = true
			status.NotAfter = cert.NotAfter
			status.Path = path
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func readCaddyCertificate(host string) (*x509.Certificate, string, error) {
	path, err := findCaddyCertificatePath(host)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, "", fmt.Errorf("certificate %s has no PEM certificate block", path)
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse certificate %s: %v", path, err)
		}
		return cert, path, nil
	}
}

func findCaddyCertificatePath(host string) (string, error) {
	patterns := []string{
		filepath.Join(caddyDataDir(), "caddy", "certificates", "*", host, host+".crt"),
		filepath.Join(caddyDataDir(), "certificates", "*", host, host+".crt"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", err
		}
		sort.Strings(matches)
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", os.ErrNotExist
}

type systemdUnitState struct {
	Name    string
	Path    string
	Present bool
	Active  string
	Enabled string
}

func doctorReaperTimerCheck(timerState func(string) systemdUnitState, boxTarget string) store.DoctorCheck {
	state := timerState(reaperTimerUnit)
	remediation := doctorTimerStartCommand(boxTarget, reaperTimerUnit)
	if !state.Present {
		return doctorCheck(doctorCheckReaperTimer, doctorStatusFailed, fmt.Sprintf("%s missing at %s", state.Name, state.Path), remediation)
	}
	if state.Active != "active" || state.Enabled != "enabled" {
		return doctorCheck(doctorCheckReaperTimer, doctorStatusDegraded, fmt.Sprintf("%s present, active=%s, enabled=%s", state.Name, state.Active, state.Enabled), remediation)
	}
	return doctorCheck(doctorCheckReaperTimer, doctorStatusOK, fmt.Sprintf("%s present, active, enabled", state.Name), doctorRerunCommand(boxTarget))
}

func systemdTimerState(name string) systemdUnitState {
	path := filepath.Join(systemdUnitDir(), name)
	state := systemdUnitState{
		Name:    name,
		Path:    path,
		Active:  host.SystemServiceStatus(name),
		Enabled: systemdEnabledStatus(name),
	}
	if _, err := os.Stat(path); err == nil {
		state.Present = true
	}
	return state
}

func systemdEnabledStatus(name string) string {
	cmd := exec.Command(utils.SystemctlBin(), "is-enabled", name)
	output, err := cmd.CombinedOutput()
	value := strings.TrimSpace(string(output))
	if value != "" {
		return value
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("exit %d", exitErr.ExitCode())
		}
		return "error"
	}
	return "enabled"
}

func doctorDeployJournalsCheck(appEnvs func() ([]appEnvStatus, error), boxTarget string) store.DoctorCheck {
	apps, err := appEnvs()
	if err != nil {
		return doctorCheck(doctorCheckDeployJournals, doctorStatusFailed, fmt.Sprintf("cannot list app envs: %v", err), doctorRerunCommand(boxTarget))
	}
	if len(apps) == 0 {
		return doctorCheck(doctorCheckDeployJournals, doctorStatusOK, "0 app envs", doctorRerunCommand(boxTarget))
	}

	var readable []string
	var findings []string
	var firstBadPath string
	for _, app := range apps {
		entries, err := readDeployJournalEntries(app.App, app.Env)
		name := app.App + "/" + app.Env
		if err != nil {
			path := identity.DeployJournalFile(app.App, app.Env)
			if firstBadPath == "" {
				firstBadPath = path
			}
			findings = append(findings, fmt.Sprintf("%s: %s", name, singleLine(err.Error())))
			continue
		}
		readable = append(readable, fmt.Sprintf("%s (%d entries)", name, len(entries)))
	}
	if len(findings) > 0 {
		return doctorCheck(doctorCheckDeployJournals, doctorStatusFailed, strings.Join(findings, "; "), doctorJournalPermissionsCommand(boxTarget, firstBadPath))
	}
	return doctorCheck(doctorCheckDeployJournals, doctorStatusOK, strings.Join(readable, "; "), doctorRerunCommand(boxTarget))
}

func doctorServiceFindingsFor(desired store.HostDesired, serviceStatus func(string) string) []string {
	var findings []string
	for _, service := range requiredServicesFor(desired) {
		status := serviceStatus(service)
		if status != "active" {
			findings = append(findings, fmt.Sprintf("%s service is %s (expected active)", service, status))
		}
	}
	return findings
}

func requiredServicesFor(desired store.HostDesired) []string {
	required := []string{"caddy"}
	switch desired.Ingress.Tunnel {
	case store.TunnelCloudflare:
		required = append(required, "cloudflared")
	case store.TunnelTailscaleFunnel:
		required = append(required, "tailscaled")
	}
	sort.Strings(required)
	return required
}

func doctorCheck(id, status, evidence, remediation string) store.DoctorCheck {
	return store.DoctorCheck{
		ID:          id,
		Status:      status,
		Evidence:    singleLine(evidence),
		Remediation: remediation,
	}
}

func doctorDelta(previous []store.DoctorCheck, current []store.DoctorCheck) []store.DoctorCheck {
	previousSeverity := map[string]int{}
	for _, check := range previous {
		previousSeverity[check.ID] = doctorSeverity(check.Status)
	}
	var delta []store.DoctorCheck
	for _, check := range current {
		if doctorSeverity(check.Status) > previousSeverity[check.ID] {
			delta = append(delta, check)
		}
	}
	if delta == nil {
		return []store.DoctorCheck{}
	}
	return delta
}

func doctorSeverity(status string) int {
	switch status {
	case doctorStatusFailed:
		return 2
	case doctorStatusDegraded:
		return 1
	default:
		return 0
	}
}

func worseDoctorStatus(left, right string) string {
	if doctorSeverity(right) > doctorSeverity(left) {
		return right
	}
	return left
}

func doctorRerunCommand(target string) string {
	if target == "" {
		return "ship server doctor"
	}
	return "ship box doctor " + utils.ShellEscape(target)
}

func doctorBoxInitCommand(target string) string {
	if target == "" {
		return "ship box init <ssh-target>"
	}
	return "ship box init " + utils.ShellEscape(target)
}

func doctorSSHCommand(target, command string) string {
	if target == "" {
		return command
	}
	return "ssh " + utils.ShellEscape(target) + " " + utils.ShellEscape(command)
}

func doctorDiskCleanupCommand(target string) string {
	return doctorSSHCommand(target, "sudo podman system prune -af && sudo journalctl --vacuum-time=7d")
}

func doctorCaddyRestartCommand(target string) string {
	return doctorSSHCommand(target, "sudo systemctl restart caddy.service")
}

func doctorTimerStartCommand(target, unit string) string {
	return doctorSSHCommand(target, fmt.Sprintf("sudo systemctl enable %s && sudo systemctl start %s", utils.ShellEscape(unit), utils.ShellEscape(unit)))
}

func doctorRestartServicesCommand(target string, desired store.HostDesired) string {
	var commands []string
	for _, service := range requiredServicesFor(desired) {
		commands = append(commands, fmt.Sprintf("sudo systemctl restart %s.service", utils.ShellEscape(service)))
	}
	return doctorSSHCommand(target, strings.Join(commands, " && "))
}

func doctorJournalPermissionsCommand(target, journalPath string) string {
	if journalPath == "" {
		return doctorRerunCommand(target)
	}
	dir := filepath.Dir(journalPath)
	command := fmt.Sprintf("sudo mkdir -p %s && sudo touch %s && sudo chown root:root %s && sudo chmod 0644 %s",
		utils.ShellEscape(dir),
		utils.ShellEscape(journalPath),
		utils.ShellEscape(journalPath),
		utils.ShellEscape(journalPath),
	)
	return doctorSSHCommand(target, command)
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func formatBytes(value uint64) string {
	const gib = 1024 * 1024 * 1024
	const mib = 1024 * 1024
	switch {
	case value >= gib:
		return fmt.Sprintf("%.1f GiB", float64(value)/gib)
	case value >= mib:
		return fmt.Sprintf("%.1f MiB", float64(value)/mib)
	default:
		return fmt.Sprintf("%d B", value)
	}
}
