// Package contracttest contains reusable conformance assertions for any
// activation-records implementation, including a future in-memory fake.
package contracttest

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/fprl/ship/activationrecords"
)

type PointerStore interface {
	Read(app, env string) (activationrecords.Pointer, error)
	Publish(app, env string, pointer activationrecords.Pointer, prepare func(string) error) error
}

func AssertAtomicPointerPublish(t *testing.T, store PointerStore) {
	t.Helper()
	old := activationrecords.Pointer{Version: 2, Activation: "old-a1b2", Artifact: activationrecords.Tuple{Release: "old1234", ImageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	newPointer := activationrecords.Pointer{Version: 2, Activation: "new-a1b2", Artifact: activationrecords.Tuple{Release: "new1234", ImageID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
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

func AssertRollbackGCAgreement(t *testing.T, rollbackCandidates, gcCandidates func() (activationrecords.CandidateSet, error)) {
	t.Helper()
	rollback, err := rollbackCandidates()
	if err != nil {
		t.Fatal(err)
	}
	gc, err := gcCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rollback, gc) {
		t.Fatalf("rollback candidates = %+v, GC candidates = %+v", rollback, gc)
	}
}
