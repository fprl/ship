package helper

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/errcat"
)

func TestAgentShellAllowsHelperProtocolAndForcesPinnedMember(t *testing.T) {
	tests := []struct {
		name     string
		original string
		want     []string
	}{
		{
			name:     "plain helper",
			original: "sudo -n /usr/local/bin/ship server app list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member", "agent-role", "list", "--json"},
		},
		{
			name:     "lying fingerprint",
			original: "sudo -n /usr/local/bin/ship server app --member-fingerprint SHA256:owner list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member", "agent-role", "list", "--json"},
		},
		{
			name:     "lying member",
			original: "sudo -n /usr/local/bin/ship server approval --member owner list --json",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "approval", "--member", "agent-role", "list", "--json"},
		},
		{
			name:     "quoted helper arg",
			original: "sudo -n /usr/local/bin/ship server app apply --git-author 'Smoke <smoke@example.com>' api prod",
			want:     []string{"sudo", "-n", "/usr/local/bin/ship", "server", "app", "--member", "agent-role", "apply", "--git-author", "Smoke <smoke@example.com>", "api", "prod"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := agentShellActionFor(tt.original, "agent-role")
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
			action, err := agentShellActionFor(tt.original, "agent-role")
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
			_, err := agentShellActionFor(original, "agent-role")
			if !errcat.Is(err, errcat.CodeOperationFailed) || !strings.Contains(err.Error(), "agent_shell_refused") {
				t.Fatalf("err = %v, want agent_shell_refused operation_failed", err)
			}
		})
	}
}
