package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/errcat"
)

func TestRenderRollbackText(t *testing.T) {
	out := renderRollbackText(rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"web"}})
	if !strings.Contains(out, "Rolled back api (production) from 3333333 to 2222222") || !strings.Contains(out, "web") || !strings.Contains(out, "running") {
		t.Fatalf("rollback summary = %s", out)
	}
}

func TestRollbackJournalFailureWarnsAfterRollbackSuccess(t *testing.T) {
	oldAppend := appendRollbackDeployJournal
	appendRollbackDeployJournal = func(string, string, activationrecords.JournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() { appendRollbackDeployJournal = oldAppend })
	cmd := appRollbackCmd{App: "api", Env: "production"}
	result := rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"worker"}}
	var stdout string
	stderr := captureStderr(t, func() { stdout = captureApplyStdout(t, func() { cmd.recordRollbackSuccess(result, time.Now().UTC()) }) })
	if !strings.Contains(stderr, "warning: rollback succeeded but failed to write deploy journal ") || !strings.Contains(stderr, "cleanup/GC were skipped; next: ship box doctor") {
		t.Fatalf("journal warning = %q", stderr)
	}
	if !strings.Contains(stdout, "Rolled back api (production) from 3333333 to 2222222") {
		t.Fatalf("rollback summary = %q", stdout)
	}
}

func TestCommittedRollbackErrorsCarryStableCodesAndConvergeNextStep(t *testing.T) {
	for _, tt := range []struct {
		name string
		code errcat.Code
		err  error
		want string
	}{
		{name: "unconverged", code: errcat.CodeDeployCommittedUnconverged, err: errors.New("caddy unavailable"), want: "committed but not converged"},
		{name: "degraded", code: errcat.CodeDeployCommittedDegraded, err: committedDegradedError{Err: errors.New("durability degraded")}, want: "committed but degraded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := rollbackCommittedError(tt.err)
			if !errcat.Is(got, tt.code) {
				t.Fatalf("error = %v, want %s", got, tt.code)
			}
			if !strings.Contains(got.Error(), tt.want) || !strings.Contains(got.Error(), "next: ship converge") {
				t.Fatalf("error = %q, want human wording and converge next step", got)
			}
		})
	}
}
