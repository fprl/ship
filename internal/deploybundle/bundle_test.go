package deploybundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReceiveRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, SourceName)
	manifest := filepath.Join(root, ManifestName)
	writeTestFile(t, source, "source bytes")
	writeTestFile(t, manifest, "[app]\nname = \"api\"\n")
	bundle := filepath.Join(root, "deploy.tar")
	metadata, err := Write(bundle, source, manifest)
	if err != nil {
		t.Fatal(err)
	}

	input, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	destination := filepath.Join(root, "private")
	if err := Receive(input, metadata, destination); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{SourceName: "source bytes", ManifestName: "[app]\nname = \"api\"\n"} {
		data, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", name, data, want)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "bundle.tar")); !os.IsNotExist(err) {
		t.Fatalf("received wire bundle was not removed: %v", err)
	}
}

func TestReceiveRejectsBadFraming(t *testing.T) {
	payload := []byte("payload")
	digest := sha256.Sum256(payload)
	valid := Metadata{Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	for _, test := range []struct {
		name     string
		input    []byte
		metadata Metadata
		want     string
	}{
		{name: "short", input: payload[:3], metadata: valid, want: "ended after"},
		{name: "extra", input: append(append([]byte{}, payload...), 'x'), metadata: valid, want: "exceeds declared size"},
		{name: "checksum", input: payload, metadata: Metadata{Size: int64(len(payload)), SHA256: strings.Repeat("0", 64)}, want: "checksum mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Receive(bytes.NewReader(test.input), test.metadata, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Receive() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReceiveRejectsUnexpectedTarEntry(t *testing.T) {
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	data := []byte("nope")
	if err := tw.WriteHeader(&tar.Header{Name: "other", Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload.Bytes())
	err := Receive(bytes.NewReader(payload.Bytes()), Metadata{Size: int64(payload.Len()), SHA256: hex.EncodeToString(digest[:])}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `invalid entry "other"`) {
		t.Fatalf("Receive() = %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
