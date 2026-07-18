package deployevent

import (
	"bytes"
	"testing"
)

func TestEventRoundTripKeepsProtocolOutOfOrdinaryStderr(t *testing.T) {
	want := Event{Kind: KindLog, Phase: "build", Message: "STEP 1/4"}
	var wire bytes.Buffer
	if err := Write(&wire, want); err != nil {
		t.Fatal(err)
	}
	got, ok := Parse(wire.String())
	if !ok || got != want {
		t.Fatalf("parsed event = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := Parse("warning: disk is nearly full"); ok {
		t.Fatal("ordinary stderr was mistaken for a deploy event")
	}
}
