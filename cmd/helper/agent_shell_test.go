package helper

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestAgentShellAllowsHelperProtocolAndForcesPinnedFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		original string
		want     []string
	}{
		{
			name:     "plain helper",
			original: "sudo -n /usr/local/bin/ship server app list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "list", "--json"},
		},
		{
			name:     "lying fingerprint",
			original: "sudo -n /usr/local/bin/ship server app --member-fingerprint SHA256:owner list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "list", "--json"},
		},
		{
			name:     "lying member claim",
			original: "sudo -n /usr/local/bin/ship server approval --member owner list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "approval", "--member-fingerprint", "SHA256:agent", "list", "--json"},
		},
		{
			// box notify must reach the helper so its role check can
			// return approval_required — the agent-shell is the transport
			// gate, not the authorization boundary (§17).
			name:     "notify passes to helper role check",
			original: "sudo -n /usr/local/bin/ship server notify set https://example.com/hook",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "notify", "--member-fingerprint", "SHA256:agent", "set", "https://example.com/hook"},
		},
		{
			name:     "config passes to helper role check",
			original: "sudo -n /usr/local/bin/ship server config set notify.url https://example.com/hook",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "config", "--member-fingerprint", "SHA256:agent", "set", "notify.url", "https://example.com/hook"},
		},
		{
			name:     "quoted helper arg",
			original: "sudo -n /usr/local/bin/ship server app apply --git-author 'Smoke <smoke@example.com>' api prod",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "apply", "--git-author", "Smoke <smoke@example.com>", "api", "prod"},
		},
		{
			// Kong applies last-value-wins, so an inline `=value` form left
			// in the tail would override the pinned fingerprint and let an
			// agent key authorize as an arbitrary owner. It must be stripped.
			name:     "lying fingerprint inline equals form",
			original: "sudo -n /usr/local/bin/ship server app destroy api --member-fingerprint=SHA256:owner",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "destroy", "api"},
		},
		{
			name:     "lying member inline equals form",
			original: "sudo -n /usr/local/bin/ship server app --member=SHA256:owner destroy api",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "destroy", "api"},
		},
		{
			name:     "lying fingerprint after literal separator stays positional",
			original: "sudo -n /usr/local/bin/ship server app destroy api -- --member-fingerprint=SHA256:owner",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member-fingerprint", "SHA256:agent", "destroy", "api", "--", "--member-fingerprint=SHA256:owner"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := agentShellActionFor(tt.original, "SHA256:agent")
			if err != nil {
				t.Fatal(err)
			}
			if action.Kind != agentShellActionExec {
				t.Fatalf("kind = %s, want exec", action.Kind)
			}
			if strings.Join(action.Args, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("argv:\nwant: %#v\n got: %#v", tt.want, action.Args)
			}
		})
	}
}

func TestAgentShellAllowsDeployUploadShapes(t *testing.T) {
	tests := []struct {
		name     string
		original string
		kind     string
		path     string
	}{
		{
			name:     "prepare remote dir",
			original: "mkdir -p /tmp/ship-deploy/api-prod-abc123 && chmod 0700 /tmp/ship-deploy/api-prod-abc123",
			kind:     agentShellActionPrepareUpload,
			path:     "/tmp/ship-deploy/api-prod-abc123",
		},
		{
			name:     "cleanup remote dir",
			original: "rm -rf /tmp/ship-deploy/api-prod-abc123",
			kind:     agentShellActionCleanupUpload,
			path:     "/tmp/ship-deploy/api-prod-abc123",
		},
		{
			name:     "rsync receiver",
			original: "rsync --server -vlogDtprze.iLsfxCIvu . /tmp/ship-deploy/api-prod-abc123/source.tar",
			kind:     agentShellActionExec,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := agentShellActionFor(tt.original, "SHA256:agent")
			if err != nil {
				t.Fatal(err)
			}
			if action.Kind != tt.kind || action.Path != tt.path {
				t.Fatalf("action = %+v, want kind=%s path=%s", action, tt.kind, tt.path)
			}
		})
	}
}

func TestAgentShellRefusesInteractiveArbitraryAndInjectionCommands(t *testing.T) {
	tests := map[string]string{
		"interactive":       "",
		"arbitrary command": "ls",
		"unallowed helper":  "sudo -n /usr/local/bin/ship server env reap",
		"semicolon":         "sudo -n /usr/local/bin/ship server app list ; rm -rf /",
		"subshell":          "sudo -n /usr/local/bin/ship server app list $(rm -rf /)",
		"newline":           "sudo -n /usr/local/bin/ship server app list\nrm -rf /",
		"bad prepare path":  "mkdir -p /tmp/not-ship && chmod 0700 /tmp/not-ship",
		"bad cleanup path":  "rm -rf /tmp/ship-deploy/../../etc",
		"bad rsync path":    "rsync --server -vlogDtprze.iLsfxCIvu . /etc/passwd",
	}
	for name, original := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := agentShellActionFor(original, "SHA256:agent")
			if !errcat.Is(err, errcat.CodeOperationFailed) || !strings.Contains(err.Error(), "agent_shell_refused") {
				t.Fatalf("err = %v, want agent_shell_refused operation_failed", err)
			}
		})
	}
}
