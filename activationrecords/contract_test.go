package activationrecords_test

import (
	"path/filepath"
	"testing"

	"github.com/fprl/ship/activationrecords"
	"github.com/fprl/ship/activationrecords/contracttest"
	"github.com/fprl/ship/kernel"
)

type diskPointerStore struct{}

func (diskPointerStore) Read(app, env string) (activationrecords.Pointer, error) {
	return activationrecords.Read(app, env)
}

func (diskPointerStore) Publish(app, env string, pointer activationrecords.Pointer, prepare func(string) error) error {
	return activationrecords.PublishPrepared(app, env, pointer, prepare)
}

type diskJournalStore struct{}

func (diskJournalStore) Append(path string, entry any) error {
	return kernel.AppendJournal(path, entry)
}

func (diskJournalStore) Read(path string, decode func([]byte) error) (bool, error) {
	return kernel.ReadJournal(path, decode)
}

func TestDiskImplementationConforms(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SHIP_APPS_DIR", filepath.Join(root, "apps"))
	contracttest.AssertAtomicPointerPublish(t, diskPointerStore{})
	contracttest.AssertAppendOnlyJournal(t, diskJournalStore{}, filepath.Join(root, "journal.jsonl"))
	contracttest.AssertClosedOutcomeVocabulary(t)
	if err := activationrecords.AppendDeployJournal("api", "production", activationrecords.JournalEntry{Outcome: activationrecords.Outcome("future_outcome")}, nil); err == nil {
		t.Fatal("deploy journal accepted an unknown outcome")
	}

	pointer := activationrecords.Pointer{Version: 2, Activation: "active-a1b2", Artifact: activationrecords.Tuple{Release: "abcdef1", ImageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if err := activationrecords.Publish("api", "production", pointer); err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"bbbbbbb", "ccccccc"} {
		tuple := activationrecords.Tuple{Release: release, ImageID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		if err := activationrecords.AppendDeployJournal("api", "production", activationrecords.JournalEntry{Outcome: activationrecords.Deployed, Artifact: &tuple}, nil); err != nil {
			t.Fatal(err)
		}
	}
	contracttest.AssertCandidatePolicy(t, func(pointer activationrecords.Pointer, verifier activationrecords.ArtifactVerifier, keep int) (activationrecords.CandidateSet, error) {
		return activationrecords.VerifiedCandidates("api", "production", pointer, verifier, keep)
	})
}
