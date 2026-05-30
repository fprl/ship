package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/host"
	"github.com/fprl/simple-vps/internal/identity"
	"github.com/fprl/simple-vps/internal/secrets"
	"github.com/fprl/simple-vps/internal/store"
	"github.com/fprl/simple-vps/internal/utils"
)

type appPreflightCmd struct {
	App     string   `arg:"" help:"App name."`
	Env     string   `arg:"" help:"Env name."`
	Secrets []string `name:"secret" help:"Required secret key. Repeat for each @secret reference."`
	JSON    bool     `name:"json" help:"Emit structured JSON instead of text."`
}

type appPreflightReport struct {
	App      string   `json:"app"`
	Env      string   `json:"env"`
	Healthy  bool     `json:"healthy"`
	Findings []string `json:"findings"`
}

func (c appPreflightCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.Die(err.Error(), 1)
	}
	report := appPreflightReportFor(c.App, c.Env, c.Secrets)
	if c.JSON {
		buf, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			utils.Die(err.Error(), 1)
		}
		fmt.Println(string(buf))
	} else {
		fmt.Print(renderAppPreflightText(report))
	}
	if !report.Healthy {
		os.Exit(1)
	}
	return nil
}

func appPreflightReportFor(app, env string, requiredSecrets []string) appPreflightReport {
	findings := appPreflightFindings(app, env, requiredSecrets)
	return appPreflightReport{
		App:      app,
		Env:      env,
		Healthy:  len(findings) == 0,
		Findings: findings,
	}
}

func appPreflightFindings(app, env string, requiredSecrets []string) []string {
	var findings []string
	stateStore := store.Default()
	if installed, err := stateStore.HostInstalled(); err != nil {
		findings = append(findings, fmt.Sprintf("cannot read host install state: %v", err))
	} else if !installed {
		findings = append(findings, "host is not installed; run `simple-vps host install`")
	} else if _, err := stateStore.ReadHost(); err != nil {
		findings = append(findings, fmt.Sprintf("host install state is invalid: %v", err))
	}
	for _, tool := range []string{"podman", "rsync"} {
		if _, err := exec.LookPath(tool); err != nil {
			findings = append(findings, fmt.Sprintf("missing host tool: %s", tool))
		}
	}
	deployTmp := host.DeployTmpDir()
	if info, err := os.Stat(deployTmp); err != nil {
		findings = append(findings, fmt.Sprintf("deploy tmp dir is missing: %s; run `simple-vps setup --env %s`", deployTmp, env))
	} else if !info.IsDir() {
		findings = append(findings, fmt.Sprintf("expected %s to be a directory", deployTmp))
	} else {
		mode := info.Mode()
		if mode.Perm() != 0777 || mode&os.ModeSticky == 0 {
			findings = append(findings, fmt.Sprintf("deploy tmp dir %s must be sticky 0777", deployTmp))
		}
	}
	user := identity.SystemUser(app, env)
	if !host.CommandSucceeds("id", "-u", user) {
		findings = append(findings, fmt.Sprintf("app env is not set up: missing system user %s; run `simple-vps setup --env %s`", user, env))
	}
	for _, dir := range []string{
		identity.EnvRoot(app, env),
		identity.DataDir(app, env),
		identity.RuntimeDir(app, env),
		identity.StaticDir(app, env),
		identity.ReleaseDir(app, env),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			findings = append(findings, fmt.Sprintf("app env is not set up: missing %s; run `simple-vps setup --env %s`", dir, env))
			continue
		}
		if !info.IsDir() {
			findings = append(findings, fmt.Sprintf("expected %s to be a directory", dir))
		}
	}
	if _, err := os.Stat(identity.IdentityFile(app, env)); err != nil {
		findings = append(findings, fmt.Sprintf("app env identity is missing; run `simple-vps setup --env %s`", env))
	} else if err := validateEnvIdentityFile(app, env); err != nil {
		findings = append(findings, fmt.Sprintf("app env identity is invalid: %v; run `simple-vps setup --env %s`", err, env))
	}
	if !host.CommandSucceeds("podman", "network", "exists", identity.Network(app, env)) {
		findings = append(findings, fmt.Sprintf("app env network is missing; run `simple-vps setup --env %s`", env))
	}
	caddyRunning, err := containerRunning("caddy")
	if err != nil {
		findings = append(findings, fmt.Sprintf("cannot inspect ingress container caddy: %v", err))
	} else if !caddyRunning {
		findings = append(findings, "ingress container caddy is not running; run host install or inspect `simple-vps host doctor`")
	} else if err := validateCaddyConfigReadOnly(); err != nil {
		findings = append(findings, fmt.Sprintf("caddy config does not validate: %v", err))
	}
	for _, key := range sortedUniqueStrings(requiredSecrets) {
		if err := secrets.ValidateKey(key); err != nil {
			findings = append(findings, err.Error())
			continue
		}
		if _, err := secrets.Get(app, env, key); errors.Is(err, secrets.ErrNotFound) {
			findings = append(findings, fmt.Sprintf("missing secret %s; run `simple-vps secret set %s --env %s`", key, key, env))
		} else if err != nil {
			findings = append(findings, fmt.Sprintf("read secret %s: %v", key, err))
		}
	}
	return findings
}

func validateEnvIdentityFile(app, env string) error {
	data, err := os.ReadFile(identity.IdentityFile(app, env))
	if err != nil {
		return err
	}
	var file envIdentityFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse identity file: %v", err)
	}
	if file.Version != 1 {
		return fmt.Errorf("unsupported identity version %d", file.Version)
	}
	if file.App != app || file.Env != env || file.InfraID != identity.InfraID(app, env) {
		return fmt.Errorf("expected app=%s env=%s infra_id=%s", app, env, identity.InfraID(app, env))
	}
	return nil
}

func validateCaddyConfigReadOnly() error {
	out, err := exec.Command("podman", "exec", "caddy", "caddy", "validate", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile").CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%s", detail)
}

func containerRunning(name string) (bool, error) {
	entries, err := podmanPSAllContainers()
	if err != nil {
		return false, err
	}
	return runningContainerExists(entries, name), nil
}

func runningContainerExists(entries []containerEntry, name string) bool {
	for _, entry := range entries {
		if entry.State != "running" {
			continue
		}
		for _, existing := range entry.Names {
			if existing == name {
				return true
			}
		}
	}
	return false
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func renderAppPreflightText(report appPreflightReport) string {
	var b strings.Builder
	if report.Healthy {
		fmt.Fprintf(&b, "Preflight passed for %s (%s)\n", report.App, report.Env)
		return b.String()
	}
	fmt.Fprintf(&b, "Preflight failed for %s (%s)\n", report.App, report.Env)
	for _, finding := range report.Findings {
		fmt.Fprintf(&b, "  - %s\n", finding)
	}
	return b.String()
}
