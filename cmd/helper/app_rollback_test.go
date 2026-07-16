package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fprl/ship/internal/errcat"
)

func TestCurrentReleaseRejectsEmptyOrMixedProcesses(t *testing.T) {
	if _, err := currentRelease(nil); err == nil || !strings.Contains(err.Error(), "no processes running") {
		t.Fatalf("expected empty-processes error, got %v", err)
	}
	_, err := currentRelease([]processStatus{{Process: "web", Release: "aaa"}, {Process: "worker", Release: "bbb"}})
	if err == nil || !strings.Contains(err.Error(), "different releases") {
		t.Fatalf("expected mixed-release error, got %v", err)
	}
}

func TestSelectRollbackRelease(t *testing.T) {
	images := []imageRelease{{Release: "3333333"}, {Release: "2222222"}, {Release: "1111111"}}
	got, err := selectRollbackRelease(images, "3333333", "")
	if err != nil || got.Release != "2222222" {
		t.Fatalf("previous rollback = %+v, err=%v", got, err)
	}
	got, err = selectRollbackRelease(images, "3333333", "1111111")
	if err != nil || got.Release != "1111111" {
		t.Fatalf("explicit rollback = %+v, err=%v", got, err)
	}
}

func TestSelectRollbackReleaseErrors(t *testing.T) {
	for _, tc := range []struct{ name, current, requested, want string }{
		{"no previous", "3333333", "", "no previous release"},
		{"missing", "3333333", "2222222", "not available"},
		{"same", "3333333", "3333333", "already running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := selectRollbackRelease([]imageRelease{{Release: "3333333"}}, tc.current, tc.requested)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGCKeepSetCoversRollbackCandidatesInJournalOrder(t *testing.T) {
	entries := []deployJournalEntry{
		{Outcome: "deployed", AttemptedRelease: "aaa1111", EndedAt: "2099-01-03T00:00:00Z"},
		{Outcome: "deployed", AttemptedRelease: "ccc3333", EndedAt: "2000-01-01T00:00:00Z"},
		{Outcome: "rolled_back", AttemptedRelease: "bbb2222", EndedAt: "2000-01-02T00:00:00Z"},
	}
	history, err := releaseDeployHistory(entries)
	if err != nil {
		t.Fatal(err)
	}
	if got := history[0].Release; got != "bbb2222" {
		t.Fatalf("journal order = %q, want newest journal release", got)
	}
	candidates := retainedReleaseHistory("preview", "ccc3333", history)
	keep := map[string]bool{"ccc3333": true}
	for _, record := range history {
		if record.Release == "ccc3333" {
			continue
		}
		if len(keep) == releaseImageKeepLimit("preview")+1 {
			break
		}
		keep[record.Release] = true
	}
	for _, candidate := range candidates {
		if !keep[candidate.Release] {
			t.Fatalf("rollback candidate %q is absent from GC keep-set: keep=%v candidates=%v", candidate.Release, keep, candidates)
		}
	}
}

func TestImageReleasesFromEntriesUsesPodmanLabels(t *testing.T) {
	entries := []imageEntry{
		{Names: []string{"localhost/ship/ship-de70a215abfd:3333333"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd", "ship.release": "3333333"}},
		{Names: []string{"localhost/ship/ship-de70a215abfd:2222222"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd", "ship.release": "2222222"}},
		{Names: []string{"localhost/ship/ship-other:ignored"}, Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-other", "ship.release": "ignored"}},
		{Names: []string{"localhost/ship/ship-de70a215abfd:1111111"}, Tag: "1111111", Labels: map[string]string{"ship.app": "hello", "ship.env": "production", "ship.infra_id": "ship-de70a215abfd"}},
	}
	got := imageReleasesFromEntries("hello", "production", entries)
	if len(got) != 2 || got[0].Release != "3333333" || got[1].Release != "2222222" {
		t.Fatalf("unexpected releases: %+v", got)
	}
}

func TestRenderRollbackText(t *testing.T) {
	out := renderRollbackText(rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"web"}})
	if !strings.Contains(out, "Rolled back api (production) from 3333333 to 2222222") || !strings.Contains(out, "web") || !strings.Contains(out, "running") {
		t.Fatalf("rollback summary = %s", out)
	}
}

func TestRollbackJournalFailureWarnsAfterRollbackSuccess(t *testing.T) {
	oldAppend := appendRollbackDeployJournal
	appendRollbackDeployJournal = func(string, string, deployJournalEntry, []string) error {
		return errors.New("journal disk is read-only")
	}
	t.Cleanup(func() { appendRollbackDeployJournal = oldAppend })
	cmd := appRollbackCmd{App: "api", Env: "production"}
	result := rollbackPayload{App: "api", Env: "production", Previous: "3333333", Release: "2222222", Processes: []string{"worker"}}
	var stdout string
	stderr := captureStderr(t, func() { stdout = captureApplyStdout(t, func() { cmd.recordRollbackSuccess(result, time.Now().UTC()) }) })
	if !strings.Contains(stderr, "warning: rollback succeeded but failed to write deploy journal ") || !strings.Contains(stderr, "cleanup/GC were skipped; run ship box doctor") {
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
