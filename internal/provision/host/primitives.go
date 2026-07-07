package host

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type AptRepo struct {
	Name           string
	KeyURL         string
	KeyPath        string
	KeyFingerprint string
	KeyDearmor     bool
	SourcePath     string
	SourceLine     string
}

type User struct {
	Name         string
	PrimaryGroup string
	Shell        string
	Home         string
	System       bool
	CreateHome   bool
}

type Directory struct {
	Path  string
	Owner string
	Group string
	Mode  os.FileMode
}

type LineInFile struct {
	Path   string
	Regexp string
	Line   string
	Owner  string
	Group  string
	Mode   os.FileMode
}

type UfwRule struct {
	Rule   string
	Delete bool
}

type passwdEntry struct {
	Exists bool
	GID    string
	Home   string
	Shell  string
}

type groupEntry struct {
	Exists bool
	GID    string
}

func EnsureDirectory(apply Apply, dir Directory) (bool, error) {
	if strings.TrimSpace(dir.Path) == "" {
		return false, errors.New("directory path is required")
	}
	if dir.Mode == 0 {
		return false, fmt.Errorf("directory mode is required for %s", dir.Path)
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "stat", Args: []string{"-c", "%U\t%G\t%a\t%F", dir.Path}})
	if err != nil {
		return false, err
	}
	wantMode := fmt.Sprintf("%o", dir.Mode.Perm())
	fields := strings.Split(strings.TrimSpace(string(result.Stdout)), "\t")
	if result.ExitCode == 0 && len(fields) == 4 {
		if fields[3] != "directory" {
			return false, fmt.Errorf("%s exists and is %s, expected directory", dir.Path, fields[3])
		}
		if fields[0] == dir.Owner && fields[1] == dir.Group && fields[2] == wantMode {
			return false, nil
		}
	}
	if apply.CheckMode {
		return true, nil
	}
	args := []string{"-d", "-o", dir.Owner, "-g", dir.Group, "-m", wantMode, dir.Path}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "install", Args: args})
	if err != nil {
		return false, err
	}
	return true, requireZero(result, "install", args)
}

func EnsurePackage(apply Apply, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("package name is required")
	}

	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "dpkg-query", Args: []string{"-W", "-f=${Status}", name}})
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 && strings.Contains(string(result.Stdout), "install ok installed") {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	if err := ensureAptUpdated(apply); err != nil {
		return false, err
	}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "apt-get", Args: []string{"install", "-y", name}})
	if err != nil {
		return false, err
	}
	return true, requireZero(result, "apt-get", []string{"install", "-y", name})
}

func EnsureAptRepo(apply Apply, repo AptRepo) (bool, error) {
	if apply.Runner == nil {
		return false, errors.New("provision runner is required")
	}
	if strings.TrimSpace(repo.Name) == "" {
		return false, errors.New("apt repo name is required")
	}
	if strings.TrimSpace(repo.SourcePath) == "" || strings.TrimSpace(repo.SourceLine) == "" {
		return false, fmt.Errorf("apt repo %s source path and line are required", repo.Name)
	}

	changed := false
	if repo.KeyURL != "" || repo.KeyPath != "" || repo.KeyFingerprint != "" {
		keyChanged, err := ensureAptRepoKey(apply, repo)
		if err != nil {
			return false, err
		}
		changed = changed || keyChanged
	}

	sourceChanged, err := EnsureFile(apply, File{
		Path:    repo.SourcePath,
		Content: []byte(strings.TrimSpace(repo.SourceLine) + "\n"),
		Owner:   "root",
		Group:   "root",
		Mode:    0644,
	})
	if err != nil {
		return false, err
	}
	changed = changed || sourceChanged
	if changed && !apply.CheckMode {
		if err := markAptNeedsUpdate(apply); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func ensureAptRepoKey(apply Apply, repo AptRepo) (bool, error) {
	if strings.TrimSpace(repo.KeyURL) == "" || strings.TrimSpace(repo.KeyPath) == "" {
		return false, fmt.Errorf("apt repo %s key URL and path are required", repo.Name)
	}
	expected, err := normalizeAptKeyFingerprint(repo.KeyFingerprint)
	if err != nil {
		return false, fmt.Errorf("apt repo %s key fingerprint: %w", repo.Name, err)
	}

	keyExists := true
	var currentKey FileState
	if file, err := apply.Runner.ReadFile(apply.ContextOrBackground(), repo.KeyPath); err != nil {
		if !errors.Is(err, ErrNotExist) {
			return false, err
		}
		keyExists = false
	} else {
		currentKey = file
	}
	if keyExists {
		// gpg is a hard dependency here; RunInstall installs gnupg with the
		// essential packages before any third-party apt repo setup runs.
		trusted, err := aptKeyHasFingerprint(apply, repo.KeyPath, expected)
		if err != nil {
			return false, err
		}
		if trusted && !aptKeyNeedsDearmor(repo, currentKey) {
			return false, nil
		}
	}

	if apply.CheckMode {
		return true, nil
	}
	if err := downloadTrustedAptKey(apply, repo, expected); err != nil {
		return false, err
	}
	return true, nil
}

func aptKeyNeedsDearmor(repo AptRepo, current FileState) bool {
	// A trusted armored key still gets replaced when the repo expects the
	// canonical dearmored keyring form.
	return repo.KeyDearmor && bytes.Contains(current.Content, []byte("-----BEGIN PGP"))
}

func normalizeAptKeyFingerprint(fingerprint string) (string, error) {
	var normalized strings.Builder
	for _, r := range fingerprint {
		switch {
		case r >= '0' && r <= '9':
			normalized.WriteRune(r)
		case r >= 'a' && r <= 'f':
			normalized.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'F':
			normalized.WriteRune(r)
		case r == ':' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
		default:
			return "", fmt.Errorf("contains non-hex character %q", r)
		}
	}
	got := normalized.String()
	if len(got) != 40 && len(got) != 64 {
		return "", fmt.Errorf("must be 40 or 64 hex characters, got %d", len(got))
	}
	return got, nil
}

func aptKeyHasFingerprint(apply Apply, path string, expected string) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "gpg", Args: []string{"--show-keys", "--with-colons", "--fingerprint", path}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	return gpgOutputHasFingerprint(result.Stdout, expected), nil
}

func requireAptKeyFingerprint(apply Apply, repo AptRepo, path string, expected string) error {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "gpg", Args: []string{"--show-keys", "--with-colons", "--fingerprint", path}})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("apt repo %s key fingerprint check failed: %s", repo.Name, bytes.TrimSpace(result.Stderr))
	}
	if !gpgOutputHasFingerprint(result.Stdout, expected) {
		return fmt.Errorf("apt repo %s key fingerprint mismatch: expected %s", repo.Name, expected)
	}
	return nil
}

func gpgOutputHasFingerprint(output []byte, expected string) bool {
	wantPrimaryFingerprint := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "pub" {
			wantPrimaryFingerprint = true
			continue
		}
		if wantPrimaryFingerprint && len(fields) > 9 && fields[0] == "fpr" {
			got, err := normalizeAptKeyFingerprint(fields[9])
			if err == nil && got == expected {
				return true
			}
			wantPrimaryFingerprint = false
		}
	}
	return false
}

func downloadTrustedAptKey(apply Apply, repo AptRepo, expected string) error {
	tempDir, err := createAptRepoTempDir(apply, repo.Name)
	if err != nil {
		return err
	}
	defer cleanupAptRepoTempDir(apply, tempDir)

	keyPath := tempDir + "/key"
	installPath := keyPath
	if repo.KeyDearmor {
		installPath = tempDir + "/key.gpg"
	}

	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "curl", Args: []string{"-fsSL", repo.KeyURL, "-o", keyPath}})
	if err != nil {
		return err
	}
	if err := requireZero(result, "curl", []string{"-fsSL", repo.KeyURL, "-o", keyPath}); err != nil {
		return err
	}
	if err := requireAptKeyFingerprint(apply, repo, keyPath, expected); err != nil {
		return err
	}
	if repo.KeyDearmor {
		args := []string{"--dearmor", "--yes", "-o", installPath, keyPath}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "gpg", Args: args})
		if err != nil {
			return err
		}
		if err := requireZero(result, "gpg", args); err != nil {
			return err
		}
		if err := requireAptKeyFingerprint(apply, repo, installPath, expected); err != nil {
			return err
		}
	}
	args := []string{"-o", "root", "-g", "root", "-m", "0644", installPath, repo.KeyPath}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "install", Args: args})
	if err != nil {
		return err
	}
	return requireZero(result, "install", args)
}

func createAptRepoTempDir(apply Apply, name string) (string, error) {
	args := []string{"-d", aptRepoTempTemplate(name)}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "mktemp", Args: args})
	if err != nil {
		return "", err
	}
	if err := requireZero(result, "mktemp", args); err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(result.Stdout))
	if path == "" {
		return "", fmt.Errorf("apt repo %s temp dir creation returned empty path", name)
	}
	return path, nil
}

func cleanupAptRepoTempDir(apply Apply, path string) {
	args := []string{"-rf", "--", path}
	_, _ = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "rm", Args: args})
}

func aptRepoTempTemplate(name string) string {
	safe := strings.ToLower(strings.TrimSpace(name))
	safe = regexp.MustCompile(`[^a-z0-9_.-]+`).ReplaceAllString(safe, "-")
	safe = strings.Trim(safe, "-.")
	if safe == "" {
		safe = "repo"
	}
	return "/tmp/ship-" + safe + "-apt.XXXXXX"
}

func EnsureUser(apply Apply, user User) (bool, error) {
	user.Name = strings.TrimSpace(user.Name)
	if user.Name == "" {
		return false, errors.New("user name is required")
	}
	if user.PrimaryGroup == "" {
		user.PrimaryGroup = user.Name
	}
	if user.Shell == "" {
		user.Shell = "/bin/bash"
	}
	desiredHome := strings.TrimSpace(user.Home)
	if desiredHome == "" && user.CreateHome {
		desiredHome = "/home/" + user.Name
	}

	group, err := lookupGroup(apply, user.PrimaryGroup)
	if err != nil {
		return false, err
	}
	current, err := lookupUser(apply, user.Name)
	if err != nil {
		return false, err
	}
	primaryGroupChanged := current.Exists && (!group.Exists || (group.GID != "" && current.GID != group.GID))
	shellChanged := current.Exists && user.Shell != "" && current.Shell != user.Shell
	homeChanged := current.Exists && desiredHome != "" && current.Home != desiredHome
	changed := !group.Exists || !current.Exists || primaryGroupChanged || shellChanged || homeChanged
	if apply.CheckMode {
		return changed, nil
	}
	if !group.Exists {
		args := []string{"--system", user.PrimaryGroup}
		if !user.System {
			args = []string{user.PrimaryGroup}
		}
		result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "groupadd", Args: args})
		if err != nil {
			return false, err
		}
		if err := requireZero(result, "groupadd", args); err != nil {
			return false, err
		}
	}
	if !current.Exists {
		args := []string{"--gid", user.PrimaryGroup, "--shell", user.Shell}
		if user.System {
			args = append(args, "--system")
		}
		if user.CreateHome {
			args = append(args, "--create-home")
		} else {
			args = append(args, "--no-create-home")
		}
		if user.Home != "" {
			args = append(args, "--home-dir", user.Home)
		}
		args = append(args, user.Name)
		result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "useradd", Args: args})
		if err != nil {
			return false, err
		}
		if err := requireZero(result, "useradd", args); err != nil {
			return false, err
		}
		return changed, nil
	}

	var args []string
	if primaryGroupChanged {
		args = append(args, "--gid", user.PrimaryGroup)
	}
	if shellChanged {
		args = append(args, "--shell", user.Shell)
	}
	if homeChanged {
		args = append(args, "--home", desiredHome, "--move-home")
	}
	if len(args) > 0 {
		args = append(args, user.Name)
		result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "usermod", Args: args})
		if err != nil {
			return false, err
		}
		if err := requireZero(result, "usermod", args); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func lookupGroup(apply Apply, name string) (groupEntry, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "getent", Args: []string{"group", name}})
	if err != nil {
		return groupEntry{}, err
	}
	if result.ExitCode != 0 {
		return groupEntry{}, nil
	}
	parts := strings.SplitN(strings.TrimSpace(string(result.Stdout)), ":", 4)
	if len(parts) < 3 || parts[2] == "" {
		return groupEntry{}, fmt.Errorf("invalid group entry for %s", name)
	}
	return groupEntry{Exists: true, GID: parts[2]}, nil
}

func lookupUser(apply Apply, name string) (passwdEntry, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "getent", Args: []string{"passwd", name}})
	if err != nil {
		return passwdEntry{}, err
	}
	if result.ExitCode != 0 {
		return passwdEntry{}, nil
	}
	parts := strings.SplitN(strings.TrimSpace(string(result.Stdout)), ":", 7)
	if len(parts) < 7 {
		return passwdEntry{}, fmt.Errorf("invalid passwd entry for %s", name)
	}
	return passwdEntry{Exists: true, GID: parts[3], Home: parts[5], Shell: parts[6]}, nil
}

func EnsureGroupMembership(apply Apply, user string, group string) (bool, error) {
	user = strings.TrimSpace(user)
	group = strings.TrimSpace(group)
	if user == "" || group == "" {
		return false, errors.New("user and group are required")
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "id", Args: []string{"-nG", user}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, requireZero(result, "id", []string{"-nG", user})
	}
	for _, existing := range strings.Fields(string(result.Stdout)) {
		if existing == group {
			return false, nil
		}
	}
	if apply.CheckMode {
		return true, nil
	}
	args := []string{"-aG", group, user}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "usermod", Args: args})
	if err != nil {
		return false, err
	}
	return true, requireZero(result, "usermod", args)
}

func EnsureLineInFile(apply Apply, change LineInFile) (bool, error) {
	pattern, err := regexp.Compile(change.Regexp)
	if err != nil {
		return false, err
	}
	current, err := apply.Runner.ReadFile(apply.ContextOrBackground(), change.Path)
	if err != nil {
		if !errors.Is(err, ErrNotExist) {
			return false, err
		}
		current = FileState{Owner: change.Owner, Group: change.Group, Mode: change.Mode}
	}

	line := strings.TrimRight(change.Line, "\r\n")
	lines, hadFinalNewline := splitLines(current.Content)
	replaced := false
	changed := false
	for idx, existing := range lines {
		if pattern.MatchString(existing) {
			replaced = true
			if existing != line {
				lines[idx] = line
				changed = true
			}
			break
		}
	}
	if !replaced {
		lines = append(lines, line)
		changed = true
	}
	if current.Owner != change.Owner || current.Group != change.Group || current.Mode.Perm() != change.Mode.Perm() {
		changed = true
	}
	if !changed {
		return false, nil
	}
	content := joinLines(lines, hadFinalNewline || changed)
	return EnsureFile(apply, File{Path: change.Path, Content: content, Owner: change.Owner, Group: change.Group, Mode: change.Mode})
}

func EnsureUfwRule(apply Apply, rule UfwRule) (bool, error) {
	if strings.TrimSpace(rule.Rule) == "" {
		return false, errors.New("ufw rule is required")
	}
	applied, err := ufwRuleApplied(apply, rule)
	if err != nil {
		return false, err
	}
	if applied {
		return false, nil
	}
	args := strings.Fields(rule.Rule)
	if rule.Delete {
		// `ufw delete <rule>` prompts; --force skips it.
		args = append([]string{"--force", "delete"}, args...)
	}
	// `ufw allow <rule>` never prompts and rejects --force as
	// "Invalid syntax" on real ufw (0.36+). Don't prepend it.
	if apply.CheckMode {
		return true, nil
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "ufw", Args: args})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 && !strings.Contains(string(result.Stderr), "Could not delete non-existent rule") {
		return false, requireZero(result, "ufw", args)
	}
	if result.ExitCode != 0 || strings.Contains(string(result.Stdout), "Skipping adding existing rule") {
		return false, nil
	}
	return true, nil
}

func ufwRuleApplied(apply Apply, rule UfwRule) (bool, error) {
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "ufw", Args: []string{"status", "verbose"}})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, nil
	}
	status := strings.ToLower(string(result.Stdout))
	present := ufwStatusContainsRule(status, strings.ToLower(rule.Rule))
	if rule.Delete {
		return !present, nil
	}
	return present, nil
}

func ufwStatusContainsRule(status string, rule string) bool {
	switch rule {
	case "default deny incoming":
		return strings.Contains(status, "default: deny (incoming)")
	case "default allow outgoing":
		return strings.Contains(status, "allow (outgoing)")
	}
	fields := strings.Fields(rule)
	if len(fields) >= 2 && fields[0] == "allow" {
		return ufwStatusHasPortAction(status, fields[1], "allow")
	}
	if len(fields) >= 2 && fields[0] == "deny" {
		return ufwStatusHasPortAction(status, fields[1], "deny")
	}
	return false
}

func ufwStatusHasPortAction(status string, port string, action string) bool {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == port && fields[1] == action {
			return true
		}
	}
	return false
}

func EnsureTimezone(apply Apply, timezone string) (bool, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return false, errors.New("timezone is required")
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "timedatectl", Args: []string{"show", "-p", "Timezone", "--value"}})
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(result.Stdout)) == timezone {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "timedatectl", Args: []string{"set-timezone", timezone}})
	if err != nil {
		return false, err
	}
	return true, requireZero(result, "timedatectl", []string{"set-timezone", timezone})
}

func EnsureLocale(apply Apply, locale string) (bool, error) {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return false, errors.New("locale is required")
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "localectl", Args: []string{"status"}})
	if err != nil {
		return false, err
	}
	if strings.Contains(string(result.Stdout), "LANG="+locale) {
		return false, nil
	}
	if apply.CheckMode {
		return true, nil
	}
	result, err = apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "localectl", Args: []string{"set-locale", "LANG=" + locale}})
	if err != nil {
		return false, err
	}
	return true, requireZero(result, "localectl", []string{"set-locale", "LANG=" + locale})
}

func ensureAptUpdated(apply Apply) error {
	if apply.State != nil && apply.State.AptUpdated {
		return nil
	}
	result, err := apply.Runner.Run(apply.ContextOrBackground(), Command{Program: "apt-get", Args: []string{"update", "-y"}})
	if err != nil {
		return err
	}
	if err := requireZero(result, "apt-get", []string{"update", "-y"}); err != nil {
		return err
	}
	if apply.State != nil {
		apply.State.AptUpdated = true
	}
	return nil
}

func markAptNeedsUpdate(apply Apply) error {
	if apply.State != nil {
		apply.State.AptUpdated = false
	}
	return ensureAptUpdated(apply)
}

func splitLines(content []byte) ([]string, bool) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if text == "" {
		return nil, true
	}
	hadFinalNewline := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	return strings.Split(text, "\n"), hadFinalNewline
}

func joinLines(lines []string, finalNewline bool) []byte {
	content := strings.Join(lines, "\n")
	if finalNewline {
		content += "\n"
	}
	return []byte(content)
}
