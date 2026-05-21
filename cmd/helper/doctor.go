package helper

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fprl/simple-vps/internal/store"
)

var (
	BroadSudoRe  = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\((?:ALL|ALL:ALL)\)\s+NOPASSWD:\s*ALL$`)
	HelperSudoRe = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?)\s+ALL=\(root\)\s+NOPASSWD:\s*/usr/local/bin/simple-vps$`)
)

type doctorCmd struct{}

func (doctorCmd) Run() error {
	CmdDoctor()
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

func CmdDoctor() {
	stateFindings := doctorStateFindings(store.Default())
	identityFindings := doctorIdentityFindings()
	fmt.Println("Simple VPS doctor")
	if len(stateFindings) > 0 {
		fmt.Println("state: degraded")
		for _, f := range stateFindings {
			fmt.Printf("  - %s\n", f)
		}
	} else {
		fmt.Println("state: healthy")
	}
	if len(identityFindings) > 0 {
		fmt.Println("identity: degraded")
		for _, f := range identityFindings {
			fmt.Printf("  - %s\n", f)
		}
	} else {
		fmt.Println("identity: healthy")
	}
	if len(stateFindings) > 0 || len(identityFindings) > 0 {
		os.Exit(1)
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
	if _, err := stateStore.ReadApps(); err != nil {
		findings = append(findings, fmt.Sprintf("apps state: %v", err))
	}
	if _, err := stateStore.ReadRoutes(); err != nil {
		findings = append(findings, fmt.Sprintf("routes state: %v", err))
	}
	if _, err := stateStore.ReadCloudflare(); err != nil {
		findings = append(findings, fmt.Sprintf("cloudflare state: %v", err))
	}
	return findings
}
