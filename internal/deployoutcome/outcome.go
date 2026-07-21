// Package deployoutcome owns the stable meaning of deploy journal outcomes.
package deployoutcome

type Kind string

const (
	Converged            Kind = "converged"
	Deployed             Kind = "deployed"
	RolledBack           Kind = "rolled_back"
	CommittedUnconverged Kind = "committed_unconverged"
	CommittedDegraded    Kind = "committed_degraded"
	Failed               Kind = "failed"
	GC                   Kind = "gc"
)

func (k Kind) FailedBeforeCommit() bool { return k == Failed }

// RetainsArtifact reports that the journal entry is evidence for a committed
// artifact candidate. The artifact itself still has to verify independently.
func (k Kind) RetainsArtifact() bool {
	switch k {
	case Deployed, RolledBack, CommittedUnconverged, CommittedDegraded:
		return true
	default:
		return false
	}
}

// CompletedLifecycle is deliberately narrower than Committed: it is used by
// "latest successful deploy" views and excludes converge repair entries.
func (k Kind) CompletedLifecycle() bool {
	return k == Deployed || k == RolledBack
}
