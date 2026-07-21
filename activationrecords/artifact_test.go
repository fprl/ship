package activationrecords

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTupleDisplayIdentityUsesTheThreeArtifactShapes(t *testing.T) {
	image := strings.Repeat("a", 64)
	static := strings.Repeat("b", 64)
	for _, test := range []struct {
		name  string
		tuple Tuple
		want  string
	}{
		{"container", Tuple{Release: "rel", ImageID: image}, "rel@" + image[:12]},
		{"static", Tuple{Release: "rel", StaticHash: static}, "rel@" + static[:12]},
		{"hybrid", Tuple{Release: "rel", ImageID: image, StaticHash: static}, "rel@" + image[:12] + "+" + static[:12]},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.tuple.DisplayIdentity(); got != test.want {
				t.Fatalf("display identity = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateArtifactRejectsWhitespacePaddedImageID(t *testing.T) {
	imageID := strings.Repeat("a", 64)
	tuple := Tuple{Release: "abcdef1", ImageID: " sha256:" + imageID + " "}
	if err := ValidateArtifact(tuple); err == nil {
		t.Fatal("ValidateArtifact() accepted a whitespace-padded image id")
	}
}

func TestStaticTreeHashIsOrderIndependentAndIncludesNestedShipReleaseFiles(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "nested", "b.txt"), []byte("bb"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "nested", "b.txt"), []byte("bb"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "nested", ".ship-release-"+strings.Repeat("c", 64)), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "nested", ".ship-release-"+strings.Repeat("c", 64)), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	hashOne, err := StaticTreeHash(first)
	if err != nil {
		t.Fatal(err)
	}
	hashTwo, err := StaticTreeHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if hashOne != hashTwo {
		t.Fatalf("same tree hashes differ: %s != %s", hashOne, hashTwo)
	}
	if err := os.WriteFile(filepath.Join(second, "nested", ".ship-release-"+strings.Repeat("c", 64)), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	hashTwo, err = StaticTreeHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if hashOne == hashTwo {
		t.Fatal("nested .ship-release file content change did not change tree hash")
	}
}

func TestStaticTreeHashRejectsUnsafeAndUnsupportedEntries(t *testing.T) {
	t.Run("absolute symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := StaticTreeHash(root); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("error = %v, want absolute-target rejection", err)
		}
	})
	t.Run("escaping symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("../outside", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := StaticTreeHash(root); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("error = %v, want tree-escape rejection", err)
		}
	})
	t.Run("control character", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "bad\nname"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := StaticTreeHash(root); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Fatalf("error = %v, want control-character rejection", err)
		}
	})
	t.Run("special file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "dir"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := StaticTreeHash(root); err != nil {
			t.Fatal(err)
		}
	})
}
