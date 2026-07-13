package client

import (
	"testing"
	"time"
)

func TestResolveDataSaveOutputPathUsesExplicitPathWithoutReleaseLookup(t *testing.T) {
	path := "/tmp/snapshot.data.tar.gz"
	got, err := resolveDataSaveOutputPath(readContext{}, path, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("output path = %q, want %q", got, path)
	}
}
