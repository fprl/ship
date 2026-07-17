// Package artifact owns the immutable identity and hashing rules for release
// artifacts.  Runtime verbs must compare these values as a tuple; no
// release-derived name is a trust reference.
package artifact

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Tuple is the exact artifact identity persisted by the v2 pointer and
// journal. EnvelopeHash is used only by static-only artifacts.
type Tuple struct {
	Release      string `json:"release"`
	ImageID      string `json:"image_id,omitempty"`
	EnvelopeHash string `json:"envelope_hash,omitempty"`
	StaticHash   string `json:"static_hash,omitempty"`
}

func (t Tuple) IsStaticOnly() bool { return t.ImageID == "" && t.StaticHash != "" }
func (t Tuple) HasImage() bool     { return t.ImageID != "" }

// DisplayIdentity is deliberately the only place that truncates hashes.
func (t Tuple) DisplayIdentity() string {
	image := imagePrefix(t.ImageID)
	static := hashPrefix(t.StaticHash)
	switch {
	case image != "" && static != "":
		return fmt.Sprintf("%s@%s+%s", t.Release, image, static)
	case image != "":
		return fmt.Sprintf("%s@%s", t.Release, image)
	case static != "":
		return fmt.Sprintf("%s@%s", t.Release, static)
	default:
		return t.Release
	}
}

func imagePrefix(value string) string {
	value = strings.TrimPrefix(value, "sha256:")
	return hashPrefix(value)
}

func hashPrefix(value string) string {
	if len(value) < 12 {
		return value
	}
	return value[:12]
}

func FullHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// StaticTreeHash hashes the tree as a length-delimited, sorted listing.
// Sidecars are intentionally outside the tree, so every file under root is
// part of the hash at every depth.
func StaticTreeHash(root string) (string, error) {
	var records []treeRecord
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if hasControlCharacter(rel) {
			return fmt.Errorf("static tree path contains a control character: %q", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := validateSymlink(root, path, target); err != nil {
				return err
			}
			records = append(records, treeRecord{path: rel, kind: 'l', target: filepath.ToSlash(target)})
		case info.IsDir():
			records = append(records, treeRecord{path: rel, kind: 'd'})
		case info.Mode().IsRegular():
			sum, size, err := fileDigest(path)
			if err != nil {
				return err
			}
			records = append(records, treeRecord{path: rel, kind: 'f', size: size, digest: sum})
		default:
			return fmt.Errorf("unsupported static tree entry %q", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].path < records[j].path })
	h := sha256.New()
	for _, record := range records {
		writeField(h, []byte{record.kind})
		writeField(h, []byte(record.path))
		switch record.kind {
		case 'f':
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(record.size))
			writeField(h, size[:])
			writeField(h, record.digest[:])
		case 'l':
			writeField(h, []byte(record.target))
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type treeRecord struct {
	path   string
	kind   byte
	target string
	size   int64
	digest [32]byte
}

func writeField(w interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(value)
}

func fileDigest(path string) ([32]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, err
	}
	defer file.Close()
	h := sha256.New()
	size, err := io.Copy(h, file)
	if err != nil {
		return [32]byte{}, 0, err
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, size, nil
}

func validateSymlink(root, path, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("static tree symlink %q has an absolute target", filepath.ToSlash(path))
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("static tree symlink %q escapes the tree", filepath.ToSlash(path))
	}
	return nil
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
