package helper

import (
	"errors"
	"fmt"
	"os"

	"github.com/fprl/ship/activationrecords"
)

type artifactCandidate struct {
	Tuple    activationrecords.Tuple
	Resolved resolvedArtifact
}

type candidateSet struct {
	Active    activationrecords.Tuple
	All       []artifactCandidate
	Verified  []artifactCandidate
	Protected []activationrecords.Tuple
	Absent    []activationrecords.Tuple
	Torn      bool
}

type artifactCandidateVerifier struct {
	resolved map[activationrecords.Tuple]resolvedArtifact
}

func (v artifactCandidateVerifier) Verify(app, env string, tuple activationrecords.Tuple) error {
	resolved, err := resolveArtifact(app, env, tuple)
	if err == nil {
		v.resolved[tuple] = resolved
	}
	return err
}

func (artifactCandidateVerifier) StaticPath(app, env string, tuple activationrecords.Tuple) string {
	return staticReleasePath(app, env, tuple.Release, tuple.StaticHash)
}

func (artifactCandidateVerifier) IsAbsent(err error) bool {
	var absent *artifactAbsentError
	return errors.As(err, &absent)
}

func (v artifactCandidateVerifier) resolvedArtifact(tuple activationrecords.Tuple) (resolvedArtifact, bool) {
	resolved, ok := v.resolved[tuple]
	return resolved, ok
}

// sharedArtifactCandidates is the only retention/rollback candidate policy.
// It verifies before applying the existing N limit, so broken newest history
// cannot consume quota.
func sharedArtifactCandidatesWithPointer(app, env string, pointer activationrecords.Pointer) (candidateSet, error) {
	verifier := artifactCandidateVerifier{resolved: make(map[activationrecords.Tuple]resolvedArtifact)}
	policy, err := activationrecords.VerifiedCandidates(app, env, pointer, verifier, releaseImageKeepLimit(env))
	if err != nil {
		return candidateSet{}, err
	}
	set := candidateSet{Active: policy.Active, Torn: policy.Torn, Protected: policy.Protected, Absent: policy.Absent}
	for _, candidate := range policy.All {
		resolved, _ := verifier.resolvedArtifact(candidate.Tuple)
		set.All = append(set.All, artifactCandidate{Tuple: candidate.Tuple, Resolved: resolved})
	}
	for _, candidate := range policy.Verified {
		resolved, _ := verifier.resolvedArtifact(candidate.Tuple)
		set.Verified = append(set.Verified, artifactCandidate{Tuple: candidate.Tuple, Resolved: resolved})
	}
	return set, nil
}

func retainedArtifactForRollback(app, env, requested string, pointer activationrecords.Pointer) (artifactCandidate, error) {
	set, err := sharedArtifactCandidatesWithPointer(app, env, pointer)
	if err != nil {
		return artifactCandidate{}, err
	}
	if requested == "" && set.Torn {
		return artifactCandidate{}, fmt.Errorf("history incomplete; pass an explicit release")
	}
	for _, candidate := range set.Verified {
		if requested == "" || candidate.Tuple.Release == requested {
			return candidate, nil
		}
	}
	if requested != "" {
		return artifactCandidate{}, fmt.Errorf("release %s is not available in committed verified history", requested)
	}
	return artifactCandidate{}, fmt.Errorf("no previous verified artifact available locally")
}

func isMissingPath(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}
