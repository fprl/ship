package helper

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/errcat"
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
	App      string              `json:"app"`
	Env      string              `json:"env"`
	Healthy  bool                `json:"healthy"`
	Issues   []appPreflightIssue `json:"issues"`
	Findings []string            `json:"findings"`
}

type appPreflightIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c appPreflightCmd) Run() error {
	if err := validateAppEnv(c.App, c.Env); err != nil {
		utils.DieError(err, 1)
	}
	report := appPreflightReportFor(c.App, c.Env, c.Secrets)
	if c.JSON {
		buf, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			utils.DieError(err, 1)
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
	issues := appPreflightIssues(app, env, requiredSecrets)
	findings := make([]string, 0, len(issues))
	for _, issue := range issues {
		findings = append(findings, issue.Message)
	}
	return appPreflightReport{
		App:      app,
		Env:      env,
		Healthy:  len(issues) == 0,
		Issues:   issues,
		Findings: findings,
	}
}

const (
	appPreflightHostNotInstalled = string(errcat.CodeHostNotInstalled)
	appPreflightHostInvalid      = string(errcat.CodeHostInvalid)
	appPreflightMissingTool      = string(errcat.CodeMissingTool)
	appPreflightDeployTmpMissing = string(errcat.CodeDeployTmpMissing)
	appPreflightDeployTmpInvalid = string(errcat.CodeDeployTmpInvalid)
	appPreflightEnvMissing       = string(errcat.CodeEnvMissing)
	appPreflightEnvInvalid       = string(errcat.CodeEnvInvalid)
	appPreflightIngressInvalid   = string(errcat.CodeIngressInvalid)
	appPreflightSecretMissing    = string(errcat.CodeSecretMissing)
	appPreflightSecretInvalid    = string(errcat.CodeSecretInvalid)
	appPreflightSecretReadError  = string(errcat.CodeSecretReadError)
)

func appPreflightIssues(app, env string, requiredSecrets []string) []appPreflightIssue {
	var issues []appPreflightIssue
	addIssue := func(code, message string) {
		issues = append(issues, appPreflightIssue{Code: code, Message: message})
	}
	stateStore := store.Default()
	if installed, err := stateStore.HostInstalled(); err != nil {
		addIssue(appPreflightHostInvalid, fmt.Sprintf("cannot read host install state: %v", err))
	} else if !installed {
		addIssue(appPreflightHostNotInstalled, "host is not installed; run `ship box init <ssh-target>`")
	} else if _, err := stateStore.ReadHost(); err != nil {
		addIssue(appPreflightHostInvalid, fmt.Sprintf("host install state is invalid: %v", err))
	}
	for _, tool := range []string{"podman", "rsync"} {
		if _, err := exec.LookPath(tool); err != nil {
			addIssue(appPreflightMissingTool, fmt.Sprintf("missing host tool: %s", tool))
		}
	}
	deployTmp := host.DeployTmpDir()
	if info, err := os.Stat(deployTmp); err != nil {
		addIssue(appPreflightDeployTmpMissing, fmt.Sprintf("deploy tmp dir is missing: %s; run `ship box init <ssh-target>`", deployTmp))
	} else if !info.IsDir() {
		addIssue(appPreflightDeployTmpInvalid, fmt.Sprintf("expected %s to be a directory", deployTmp))
	} else {
		mode := info.Mode()
		if mode.Perm() != 0777 || mode&os.ModeSticky == 0 {
			addIssue(appPreflightDeployTmpInvalid, fmt.Sprintf("deploy tmp dir %s must be sticky 0777", deployTmp))
		}
	}
	user := identity.SystemUser(app, env)
	if !host.CommandSucceeds("id", "-u", user) {
		addIssue(appPreflightEnvMissing, fmt.Sprintf("app env is not prepared: missing system user %s", user))
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
			addIssue(appPreflightEnvMissing, fmt.Sprintf("app env is not prepared: missing %s", dir))
			continue
		}
		if !info.IsDir() {
			addIssue(appPreflightEnvInvalid, fmt.Sprintf("expected %s to be a directory", dir))
		}
	}
	if _, err := os.Stat(identity.IdentityFile(app, env)); err != nil {
		addIssue(appPreflightEnvMissing, "app env identity is missing")
	} else if err := validateEnvIdentityFile(app, env); err != nil {
		addIssue(appPreflightEnvInvalid, fmt.Sprintf("app env identity is invalid: %v", err))
	}
	if !host.CommandSucceeds("podman", "network", "exists", identity.Network(app, env)) {
		addIssue(appPreflightEnvMissing, "app env network is missing")
	}
	caddyRunning, err := containerRunning("caddy")
	if err != nil {
		addIssue(appPreflightIngressInvalid, fmt.Sprintf("cannot inspect ingress container caddy: %v", err))
	} else if !caddyRunning {
		addIssue(appPreflightIngressInvalid, "ingress container caddy is not running; run `ship box doctor`")
	} else if err := validateCaddyConfigReadOnly(); err != nil {
		addIssue(appPreflightIngressInvalid, fmt.Sprintf("caddy config does not validate: %v", err))
	}
	for _, key := range sortedUniqueStrings(requiredSecrets) {
		if err := secrets.ValidateKey(key); err != nil {
			addIssue(appPreflightSecretInvalid, err.Error())
			continue
		}
		if _, err := resolveSecretValue(app, env, key); err != nil && errcat.Is(err, errcat.CodeSecretMissing) {
			addIssue(appPreflightSecretMissing, preflightIssueMessage(err))
		} else if err != nil {
			addIssue(appPreflightSecretReadError, fmt.Sprintf("read secret %s: %v", key, err))
		}
	}
	return issues
}

func preflightIssueMessage(err error) string {
	if coded, ok := errcat.As(err); ok {
		return fmt.Sprintf("%s; run `%s`", coded.Cause(), coded.Remediation())
	}
	return err.Error()
}

func validateEnvIdentityFile(app, env string) error {
	data, err := os.ReadFile(identity.IdentityFile(app, env))
	if err != nil {
		return err
	}
	return validateEnvIdentityData(app, env, data)
}

func validateEnvIdentityData(app, env string, data []byte) error {
	var file identity.EnvIdentity
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
