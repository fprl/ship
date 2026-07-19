// Package sourcearchive safely extracts the source tar produced by the Ship
// client into a helper-owned build context.
package sourcearchive

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Extract(archivePath, destination string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}

	seen := map[string]byte{}
	symlinks := map[string]string{}
	tr := tar.NewReader(archive)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read source archive: %w", err)
		}
		// GNU tar can emit a global PAX metadata record before ordinary
		// entries. archive/tar already applies its fields to later headers;
		// those effective names and types still pass through the checks below.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, err := safeRelativePath(header.Name)
		if err != nil {
			return err
		}
		path := filepath.Join(destination, filepath.FromSlash(rel))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := acceptEntryKind(seen, rel, tar.TypeDir); err != nil {
				return err
			}
			if err := os.MkdirAll(path, directoryMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := acceptEntryKind(seen, rel, tar.TypeReg); err != nil {
				return err
			}
			if header.Size < 0 {
				return fmt.Errorf("source archive contains invalid size for %q", header.Name)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode(header.Mode))
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(out, tr, header.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("extract %s after %d bytes: %w", header.Name, written, copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := acceptEntryKind(seen, rel, tar.TypeSymlink); err != nil {
				return err
			}
			if err := validateSymlinkTarget(rel, header.Linkname); err != nil {
				return err
			}
			symlinks[path] = header.Linkname
		default:
			return fmt.Errorf("source archive contains unsupported entry %q", header.Name)
		}
	}
	// Links are installed last, so regular-file extraction can never follow a
	// symlink supplied earlier in the archive.
	for path, target := range symlinks {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.Symlink(target, path); err != nil {
			return err
		}
	}
	return nil
}

func acceptEntryKind(seen map[string]byte, path string, kind byte) error {
	if previous, ok := seen[path]; ok && previous != kind {
		return fmt.Errorf("source archive changes entry type for %q", path)
	}
	seen[path] = kind
	return nil
}

func safeRelativePath(name string) (string, error) {
	name = filepath.ToSlash(name)
	clean := filepath.ToSlash(filepath.Clean(name))
	if name == "" || clean == "." || filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("source archive path escapes build context: %q", name)
	}
	return clean, nil
}

func validateSymlinkTarget(linkPath, target string) error {
	if target == "" || filepath.IsAbs(target) {
		return fmt.Errorf("source archive symlink escapes build context: %q -> %q", linkPath, target)
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(linkPath), target)))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("source archive symlink escapes build context: %q -> %q", linkPath, target)
	}
	return nil
}

func directoryMode(mode int64) os.FileMode {
	permissions := os.FileMode(mode) & 0777
	if permissions == 0 {
		return 0755
	}
	return permissions
}

func fileMode(mode int64) os.FileMode {
	return os.FileMode(mode) & 0777
}
