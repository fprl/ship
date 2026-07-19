package sourcearchive

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractRegularFilesAndInternalSymlink(t *testing.T) {
	archive := writeArchive(t,
		tarEntry{name: "bin/run", body: "#!/bin/sh\n", mode: 0755, kind: tar.TypeReg},
		tarEntry{name: "run", target: "bin/run", kind: tar.TypeSymlink},
	)
	destination := t.TempDir()
	if err := Extract(archive, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\n" {
		t.Fatalf("linked file = %q", data)
	}
}

func TestExtractUsesLastCopyOfRepeatedRegularFile(t *testing.T) {
	archive := writeArchive(t,
		tarEntry{name: "dist/app.js", body: "old", mode: 0644, kind: tar.TypeReg},
		tarEntry{name: "dist/app.js", body: "built", mode: 0644, kind: tar.TypeReg},
	)
	destination := t.TempDir()
	if err := Extract(archive, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "dist/app.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "built" {
		t.Fatalf("repeated file = %q, want final archive value", data)
	}
}

func TestExtractAcceptsGlobalPAXMetadata(t *testing.T) {
	archive := writeArchive(t,
		tarEntry{name: "pax_global_header", kind: tar.TypeXGlobalHeader},
		tarEntry{name: "app.js", body: "ok", mode: 0644, kind: tar.TypeReg},
	)
	destination := t.TempDir()
	if err := Extract(archive, destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "app.js")); err != nil || string(data) != "ok" {
		t.Fatalf("app.js = %q, err=%v", data, err)
	}
}

func TestExtractRejectsEscapesAndSpecialEntries(t *testing.T) {
	for _, test := range []struct {
		name string
		item tarEntry
		want string
	}{
		{name: "parent path", item: tarEntry{name: "../outside", body: "owned", kind: tar.TypeReg}, want: "path escapes"},
		{name: "absolute path", item: tarEntry{name: "/outside", body: "owned", kind: tar.TypeReg}, want: "path escapes"},
		{name: "escaping symlink", item: tarEntry{name: "link", target: "../../etc/passwd", kind: tar.TypeSymlink}, want: "symlink escapes"},
		{name: "device", item: tarEntry{name: "device", kind: tar.TypeChar}, want: "unsupported entry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Extract(writeArchive(t, test.item), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Extract() = %v, want %q", err, test.want)
			}
		})
	}
}

type tarEntry struct {
	name   string
	body   string
	target string
	mode   int64
	kind   byte
}

func writeArchive(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Linkname: entry.target, Mode: entry.mode, Size: int64(len(entry.body)), Typeflag: entry.kind}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.tar")
	if err := os.WriteFile(path, payload.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
