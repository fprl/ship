package helper

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeployCommandFailureShowsScrubbedTailWithoutLogsFlag(t *testing.T) {
	emitter := newDeployProgressEmitter(true, false, &bytes.Buffer{})
	stderr := captureStderr(t, func() {
		_, _ = runDeployCommand(emitter, "release", []string{"secret-token"}, 0, "sh", []string{"-c", "echo before; echo secret-token >&2; exit 9"}, "")
	})
	if strings.Contains(stderr, "secret-token") {
		t.Fatalf("automatic failure tail leaked a secret: %s", stderr)
	}
	if !strings.Contains(stderr, "before") || !strings.Contains(stderr, "[redacted]") {
		t.Fatalf("automatic failure tail lost useful output: %s", stderr)
	}
}

func TestDeployCommandLogsAreStructuredAndScrubbed(t *testing.T) {
	var wire bytes.Buffer
	emitter := newDeployProgressEmitter(true, true, &wire)
	if _, err := runDeployCommand(emitter, "release", []string{"secret-token"}, 0, "sh", []string{"-c", "echo migrating; echo secret-token"}, ""); err != nil {
		t.Fatal(err)
	}
	got := wire.String()
	if strings.Contains(got, "secret-token") {
		t.Fatalf("structured logs leaked a secret: %s", got)
	}
	if !strings.Contains(got, `"kind":"log"`) || !strings.Contains(got, "migrating") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("structured logs missing expected lines: %s", got)
	}
}
