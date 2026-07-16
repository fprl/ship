package provision

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fprl/ship/internal/provision/host"
)

// ManagedLine is a line that provisioning converges in a managed file.
// Pattern and Line mirror host.LineInFile's inputs so readers such as doctor
// use the same matching contract as convergence.
type ManagedLine struct {
	Path    string
	Pattern string
	Line    string
}

// ManagedFile is a managed block or exact file written by provisioning.
type ManagedFile struct {
	Path    string
	Kind    string
	Content string
}

const (
	ManagedFileBlock = "block"
	ManagedFileExact = "exact"
)

func sshHardeningExpectations() []ManagedLine {
	return []ManagedLine{
		{Path: "/etc/ssh/sshd_config", Pattern: `^#?PermitRootLogin\b`, Line: "PermitRootLogin prohibit-password"},
		{Path: "/etc/ssh/sshd_config", Pattern: `^#?PasswordAuthentication\b`, Line: "PasswordAuthentication no"},
		{Path: "/etc/ssh/sshd_config", Pattern: `^#?PubkeyAuthentication\b`, Line: "PubkeyAuthentication yes"},
		{Path: "/etc/ssh/sshd_config", Pattern: `^#?X11Forwarding\b`, Line: "X11Forwarding no"},
		{Path: "/etc/ssh/sshd_config", Pattern: `^#?MaxAuthTries\b`, Line: "MaxAuthTries 3"},
	}
}

func firewallRules() []string {
	return []string{
		"default deny incoming",
		"default allow outgoing",
		"allow 22/tcp",
		"allow 80/tcp",
		"allow 443/tcp",
	}
}

func firewallForwardPolicyExpectation() ManagedLine {
	return ManagedLine{
		Path:    "/etc/default/ufw",
		Pattern: `^DEFAULT_FORWARD_POLICY=`,
		Line:    `DEFAULT_FORWARD_POLICY="ACCEPT"`,
	}
}

func shipSudoersContent() string {
	return fmt.Sprintf("%s ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *, /usr/local/bin/ship server approval *, /usr/local/bin/ship server config *, /usr/local/bin/ship server webhook *, /usr/local/bin/ship server version, /usr/local/bin/ship server version *, /usr/local/bin/ship server update *\n", defaultDeployUser)
}

// SSHHardeningExpectations returns the exact managed sshd_config values.
func SSHHardeningExpectations() []ManagedLine {
	return append([]ManagedLine(nil), sshHardeningExpectations()...)
}

// FirewallRules returns the UFW rules provisioner ensures through `ufw`.
func FirewallRules() []string {
	return append([]string(nil), firewallRules()...)
}

// FirewallFileExpectations returns the file-backed firewall state managed by
// provisioning. Unmanaged distro and operator rules are intentionally not
// included: provisioning preserves them.
func FirewallFileExpectations() []ManagedLine {
	return []ManagedLine{firewallForwardPolicyExpectation()}
}

// FirewallBeforeRulesExpectation returns the marked Podman bridge block.
func FirewallBeforeRulesExpectation() ManagedFile {
	return ManagedFile{
		Path:    "/etc/ufw/before.rules",
		Kind:    ManagedFileBlock,
		Content: "# BEGIN " + podmanUfwMarker + "\n" + strings.TrimRight(podmanUfwBlock(), "\n") + "\n# END " + podmanUfwMarker,
	}
}

// ShipSudoersExpectation returns the exact sudoers file written by
// provisioning.
func ShipSudoersExpectation() ManagedFile {
	return ManagedFile{Path: "/etc/sudoers.d/ship", Kind: ManagedFileExact, Content: shipSudoersContent()}
}

// FirewallRulePresent uses the same UFW status interpretation as convergence.
func FirewallRulePresent(status, rule string) bool {
	return host.UfwStatusContainsRuleForDoctor(status, rule)
}

// MatchManagedLine applies the same first-match semantics as EnsureLineInFile.
func MatchManagedLine(content string, expectation ManagedLine) bool {
	pattern, err := regexp.Compile(expectation.Pattern)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		if pattern.MatchString(line) {
			return line == expectation.Line
		}
	}
	return false
}
