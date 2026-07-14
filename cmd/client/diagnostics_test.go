package client

import (
	"testing"

	"github.com/fprl/ship/internal/config"
)

func TestManifestDiagnosticsLeaveHintsEmpty(t *testing.T) {
	diags := manifestDiagnostics([]string{"invalid manifest setting", config.DockerfileMissingDetail}, []string{"manifest warning"})
	for _, diag := range diags {
		if diag.Hint != "" {
			t.Fatalf("manifest diagnostic %q has hint %q, want empty", diag.Message, diag.Hint)
		}
	}
}
