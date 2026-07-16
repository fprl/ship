package client

import (
	"strings"
	"testing"

	"github.com/fprl/ship/internal/config"
)

func TestStage5ClientCommandsAndRemediation(t *testing.T) {
	if got := serverAppConvergeCommand("api", "production", true); !strings.Contains(got, "server app converge --json api production") {
		t.Fatalf("app converge command=%q", got)
	}
	if got := serverGCCommand(true); got != "sudo -n /usr/local/bin/ship server gc --json" {
		t.Fatalf("GC command=%q", got)
	}
	if convergenceNextStep != "ship converge" {
		t.Fatalf("convergence remediation=%q", convergenceNextStep)
	}
	read := readContext{AppContext: &config.AppContext{AppName: "api", ProductionBranch: "main"}, EnvName: "production", Address: readAddress{ProductionBranch: true}}
	if got := rewriteEnvSummary("Converged api (production) at abc123\n", read, "Converged"); !strings.Contains(got, "Converged Production main") {
		t.Fatalf("rewritten converge summary=%q", got)
	}
}
