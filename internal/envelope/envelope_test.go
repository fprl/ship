package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopePreservesManifestAndMetadataThroughLabel(t *testing.T) {
	manifest := []byte("name = \"api\"\n# verbatim\n")
	metadata := map[string]any{"schema_version": 1, "release": "abc1234"}
	e, err := New(manifest, metadata)
	if err != nil {
		t.Fatal(err)
	}
	label, err := e.LabelValue()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLabel(label)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Manifest != string(manifest) {
		t.Fatalf("manifest = %q, want %q", decoded.Manifest, manifest)
	}
	var got map[string]any
	if err := json.Unmarshal(decoded.Metadata, &got); err != nil || got["release"] != "abc1234" {
		t.Fatalf("metadata = %s, err=%v", decoded.Metadata, err)
	}
}

func TestEnvelopeRejectsManifestOver64KiB(t *testing.T) {
	_, err := New([]byte(strings.Repeat("x", ManifestLimit+1)), map[string]string{"release": "abc1234"})
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("error = %v, want clear 64 KiB cap", err)
	}
}

func TestEnvelopeRejectsSerializedOver64KiB(t *testing.T) {
	_, err := New([]byte("name = \"api\"\n"), map[string]string{"release": "abc1234", "padding": strings.Repeat("x", SerializedLimit)})
	if err == nil || !strings.Contains(err.Error(), "serialized release envelope") {
		t.Fatalf("error = %v, want serialized 64 KiB cap", err)
	}
}

func TestDecodeRejectsOversizedDecodedManifest(t *testing.T) {
	e := Envelope{Schema: Schema, Manifest: strings.Repeat("x", ManifestLimit+1), Metadata: json.RawMessage(`{}`)}
	data, _ := json.Marshal(e)
	if _, err := DecodeJSON(data); err == nil {
		t.Fatal("expected decoded manifest cap refusal")
	}
}
