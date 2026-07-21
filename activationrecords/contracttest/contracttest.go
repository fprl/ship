// Package contracttest contains reusable conformance assertions for any
// activation-records implementation, including a future in-memory fake.
package contracttest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/internal/identity"
)

type PointerStore interface {
	Read(app, env string) (activationrecords.Pointer, error)
	Publish(app, env string, pointer activationrecords.Pointer, prepare func(string) error) error
}

func AssertAtomicPointerPublish(t *testing.T, store PointerStore) {
	t.Helper()
	old := activationrecords.Pointer{Version: 2, Activation: "old-a1b2", Artifact: activationrecords.Tuple{Release: "aaa1234", ImageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	newPointer := activationrecords.Pointer{Version: 2, Activation: "new-a1b2", Artifact: activationrecords.Tuple{Release: "bbb1234", ImageID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	if err := store.Publish("api", "production", old, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Publish("api", "production", newPointer, func(string) error { return errors.New("prepare failed") }); err == nil {
		t.Fatal("failed publish returned nil")
	}
	got, err := store.Read("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, old) {
		t.Fatalf("failed publish changed pointer: got %+v, want %+v", got, old)
	}
}

type JournalStore interface {
	Append(path string, entry any) error
	Read(path string, decode func([]byte) error) (bool, error)
}

func AssertAppendOnlyJournal(t *testing.T, store JournalStore, path string) {
	t.Helper()
	if err := store.Append(path, map[string]string{"event": "one"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(path, map[string]string{"event": "two"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	torn, err := store.Read(path, func(line []byte) error {
		var entry struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		got = append(got, entry.Event)
		return nil
	})
	if err != nil || torn {
		t.Fatalf("journal read = torn %v, err %v", torn, err)
	}
	if !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("journal records = %v, want [one two]", got)
	}
}

func AssertClosedOutcomeVocabulary(t *testing.T) {
	t.Helper()
	for _, outcome := range []activationrecords.Outcome{
		activationrecords.Converged,
		activationrecords.Deployed,
		activationrecords.RolledBack,
		activationrecords.CommittedUnconverged,
		activationrecords.CommittedDegraded,
		activationrecords.Failed,
		activationrecords.GC,
	} {
		if !activationrecords.ValidOutcome(outcome) {
			t.Fatalf("known outcome %q is not in the vocabulary", outcome)
		}
	}
	if activationrecords.ValidOutcome(activationrecords.Outcome("future_outcome")) {
		t.Fatal("unknown outcome was accepted")
	}
}

type CandidatePolicy func(pointer activationrecords.Pointer, verifier activationrecords.ArtifactVerifier, keep int) (activationrecords.CandidateSet, error)

type FakeArtifactVerifier struct {
	VerifyFunc     func(app, env string, tuple activationrecords.Tuple) error
	StaticPathFunc func(app, env string, tuple activationrecords.Tuple) string
	IsAbsentFunc   func(error) bool
}

func (f FakeArtifactVerifier) Verify(app, env string, tuple activationrecords.Tuple) error {
	if f.VerifyFunc == nil {
		return nil
	}
	return f.VerifyFunc(app, env, tuple)
}

func (f FakeArtifactVerifier) StaticPath(app, env string, tuple activationrecords.Tuple) string {
	if f.StaticPathFunc == nil {
		return ""
	}
	return f.StaticPathFunc(app, env, tuple)
}

func (f FakeArtifactVerifier) IsAbsent(err error) bool {
	if f.IsAbsentFunc == nil {
		return false
	}
	return f.IsAbsentFunc(err)
}

func AssertCandidatePolicy(t *testing.T, policy CandidatePolicy) {
	t.Helper()
	t.Run("verification precedes retention and protects uncertainty", func(t *testing.T) {
		pointer, _ := candidateFixture(t, []activationrecords.Tuple{
			{Release: "ccccccc", ImageID: stringsOf('c')},
			{Release: "bbbbbbb", ImageID: stringsOf('b')},
		})
		broken := errors.New("inspect failed")
		set, err := policy(pointer, FakeArtifactVerifier{
			VerifyFunc: func(_ string, _ string, tuple activationrecords.Tuple) error {
				if tuple.Release == "bbbbbbb" {
					return broken
				}
				return nil
			},
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.Verified) != 1 || set.Verified[0].Tuple.Release != "ccccccc" {
			t.Fatalf("verified candidates = %+v, want only ccccccc", set.Verified)
		}
		if len(set.Protected) != 1 || set.Protected[0].Release != "bbbbbbb" {
			t.Fatalf("protected candidates = %+v, want bbbbbbb", set.Protected)
		}
	})

	t.Run("absent classification short-circuits image static lookup", func(t *testing.T) {
		pointer, _ := candidateFixture(t, []activationrecords.Tuple{
			{Release: "eeeeeee", StaticHash: stringsOf('e'), EnvelopeHash: stringsOf('f')},
			{Release: "ddddddd", ImageID: stringsOf('d')},
		})
		absent := errors.New("absent")
		set, err := policy(pointer, FakeArtifactVerifier{
			VerifyFunc: func(_ string, _ string, tuple activationrecords.Tuple) error {
				if tuple.Release == "ddddddd" || tuple.Release == "eeeeeee" {
					return absent
				}
				return nil
			},
			StaticPathFunc: func(_ string, _ string, tuple activationrecords.Tuple) string {
				if tuple.StaticHash == "" {
					t.Fatal("StaticPath was called for an image-only tuple")
				}
				return filepath.Join(t.TempDir(), "missing")
			},
			IsAbsentFunc: func(err error) bool { return errors.Is(err, absent) },
		}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.Absent) != 2 || len(set.Protected) != 0 {
			t.Fatalf("absent=%+v protected=%+v, want both tuples absent", set.Absent, set.Protected)
		}
	})

	t.Run("torn history propagates", func(t *testing.T) {
		pointer, path := candidateFixture(t, []activationrecords.Tuple{{Release: "bbbbbbb", ImageID: stringsOf('b')}})
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"outcome":"deployed"}`); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		set, err := policy(pointer, FakeArtifactVerifier{}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !set.Torn {
			t.Fatal("candidate policy did not propagate torn history")
		}
	})
}

func candidateFixture(t *testing.T, entries []activationrecords.Tuple) (activationrecords.Pointer, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	pointer := activationrecords.Pointer{Version: 2, Activation: "active-a1b2", Artifact: activationrecords.Tuple{Release: "aaaaaaa", ImageID: stringsOf('a')}}
	if err := activationrecords.Publish("api", "production", pointer); err != nil {
		t.Fatal(err)
	}
	for _, tuple := range entries {
		if err := activationrecords.AppendDeployJournal("api", "production", activationrecords.JournalEntry{Outcome: activationrecords.Deployed, Artifact: &tuple}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return pointer, identity.DeployJournalFile("api", "production")
}

func stringsOf(char byte) string { return strings.Repeat(string(char), 64) }
