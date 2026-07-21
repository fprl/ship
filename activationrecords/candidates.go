package activationrecords

import (
	"fmt"
	"os"
)

// ArtifactVerifier supplies runtime-specific facts without importing a
// runtime adapter into activationrecords.
type ArtifactVerifier interface {
	Verify(app, env string, tuple Tuple) error
	StaticPath(app, env string, tuple Tuple) string
	IsAbsent(error) bool
}

type Candidate struct{ Tuple Tuple }

type CandidateSet struct {
	Active    Tuple
	All       []Candidate
	Verified  []Candidate
	Protected []Tuple
	Absent    []Tuple
	Torn      bool
}

// VerifiedCandidates is the one rollback/GC candidate policy. It walks the
// complete committed history, verifies before applying the retention limit,
// and keeps unverifiable committed references protected for conservative GC.
func VerifiedCandidates(app, env string, pointer Pointer, verifier ArtifactVerifier, keep int) (CandidateSet, error) {
	history, torn, err := CommittedHistory(app, env, pointer)
	if err != nil {
		return CandidateSet{}, err
	}
	set := CandidateSet{Active: pointer.Artifact, Torn: torn}
	for _, tuple := range history {
		if tuple == pointer.Artifact {
			continue
		}
		verifyErr := VerifyArtifact(app, env, tuple, verifier)
		if verifyErr != nil {
			staticGone := tuple.StaticHash == "" || staticMissing(verifier.StaticPath(app, env, tuple))
			if verifier.IsAbsent(verifyErr) && staticGone {
				set.Absent = append(set.Absent, tuple)
				continue
			}
			set.Protected = append(set.Protected, tuple)
			continue
		}
		candidate := Candidate{Tuple: tuple}
		set.All = append(set.All, candidate)
		set.Verified = append(set.Verified, candidate)
	}
	if keep < 0 {
		keep = 0
	}
	if len(set.Verified) > keep {
		set.Verified = set.Verified[:keep]
	}
	return set, nil
}

func VerifyArtifact(app, env string, tuple Tuple, verifier ArtifactVerifier) error {
	if err := ValidateArtifact(tuple); err != nil {
		return err
	}
	if err := verifier.Verify(app, env, tuple); err != nil {
		return err
	}
	if tuple.StaticHash != "" {
		if err := verifyStaticTree(verifier.StaticPath(app, env, tuple), tuple.StaticHash); err != nil {
			return fmt.Errorf("static artifact %s: %w", tuple.DisplayIdentity(), err)
		}
	}
	return nil
}

func staticMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}
