package helper

import (
	"errors"
	"fmt"
	"os"

	"github.com/fprl/ship/internal/activation"
	"github.com/fprl/ship/internal/artifact"
)

type artifactCandidate struct {
	Tuple    artifact.Tuple
	Resolved resolvedArtifact
}

type candidateSet struct {
	Active    artifact.Tuple
	All       []artifactCandidate
	Verified  []artifactCandidate
	Protected []artifact.Tuple
	Absent    []artifact.Tuple
	Torn      bool
}

// sharedArtifactCandidates is the only retention/rollback candidate policy.
// It verifies before applying the existing N limit, so broken newest history
// cannot consume quota.
func sharedArtifactCandidatesWithPointer(app, env string, pointer activation.Pointer) (candidateSet, error) {
	if pointer.IsLegacy() {
		return candidateSet{}, nil
	}
	history, torn, err := committedHistoryWithPointer(app, env, pointer)
	if err != nil {
		return candidateSet{}, err
	}
	set := candidateSet{Active: pointer.Artifact, Torn: torn}
	for _, tuple := range history {
		if tuple == pointer.Artifact {
			continue
		}
		resolved, resolveErr := resolveArtifact(app, env, tuple)
		if resolveErr == nil && tuple.StaticHash != "" {
			path := staticReleasePath(app, env, tuple.Release, tuple.StaticHash)
			hash, hashErr := artifact.StaticTreeHash(path)
			if hashErr != nil || hash != tuple.StaticHash {
				resolveErr = fmt.Errorf("static artifact %s hash does not match", tuple.DisplayIdentity())
			}
		}
		if resolveErr != nil {
			var absentErr *artifactAbsentError
			staticMissing := tuple.StaticHash == "" || isMissingPath(staticReleasePath(app, env, tuple.Release, tuple.StaticHash))
			if errors.As(resolveErr, &absentErr) && staticMissing {
				set.Absent = append(set.Absent, tuple)
				continue
			}
			set.Protected = append(set.Protected, tuple)
			continue
		}
		candidate := artifactCandidate{Tuple: tuple, Resolved: resolved}
		set.All = append(set.All, candidate)
		set.Verified = append(set.Verified, candidate)
	}
	limit := releaseImageKeepLimit(env)
	if len(set.Verified) > limit {
		set.Verified = set.Verified[:limit]
	}
	return set, nil
}

func retainedArtifactForRollback(app, env, requested string, pointer activation.Pointer) (artifactCandidate, error) {
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
