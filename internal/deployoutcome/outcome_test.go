package deployoutcome

import "testing"

func TestOutcomeClassification(t *testing.T) {
	tests := []struct {
		kind                       Kind
		failed, retain, completed bool
	}{
		{Converged, false, false, false},
		{Deployed, false, true, true},
		{RolledBack, false, true, true},
		{CommittedUnconverged, false, true, false},
		{CommittedDegraded, false, true, false},
		{Failed, true, false, false},
		{GC, false, false, false},
	}
	for _, tt := range tests {
		if tt.kind.FailedBeforeCommit() != tt.failed || tt.kind.RetainsArtifact() != tt.retain || tt.kind.CompletedLifecycle() != tt.completed {
			t.Fatalf("classification for %q drifted", tt.kind)
		}
	}
}
