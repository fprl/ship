// Package deploybundle owns the on-wire deploy bundle shared by the client
// and the privileged box helper.
package deploybundle

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const (
	SourceName       = "source.tar"
	ManifestName     = "ship.toml"
	MaxBundleBytes   = int64(2 << 30)
	MaxManifestBytes = int64(1 << 20)
)

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Metadata struct {
	Size   int64
	SHA256 string
}

// Write creates the exact two-file bundle accepted by Receive.
func Write(destination, source, manifest string) (metadata Metadata, err error) {
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return Metadata{}, err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(destination)
		}
	}()

	hash := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(out, hash)}
	tw := tar.NewWriter(counted)
	for _, file := range []struct {
		name string
		path string
		max  int64
	}{
		{name: SourceName, path: source, max: MaxBundleBytes},
		{name: ManifestName, path: manifest, max: MaxManifestBytes},
	} {
		if err = appendRegularFile(tw, file.name, file.path, file.max); err != nil {
			_ = tw.Close()
			return Metadata{}, err
		}
	}
	if err = tw.Close(); err != nil {
		return Metadata{}, err
	}
	if counted.n > MaxBundleBytes {
		return Metadata{}, fmt.Errorf("deploy bundle is %d bytes; maximum is %d", counted.n, MaxBundleBytes)
	}
	return Metadata{Size: counted.n, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func appendRegularFile(tw *tar.Writer, name, path string, max int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > max {
		return fmt.Errorf("%s is %d bytes; maximum is %d", name, info.Size(), max)
	}
	header := &tar.Header{Name: name, Mode: 0600, Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, file)
	return err
}

// Receive copies exactly metadata.Size bytes from input into a private
// directory, verifies the digest, then extracts only source.tar and ship.toml.
func Receive(input io.Reader, metadata Metadata, destination string) error {
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	bundlePath := filepath.Join(destination, "bundle.tar")
	bundle, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(bundle, hash), input, metadata.Size)
	closeErr := bundle.Close()
	if copyErr != nil {
		return fmt.Errorf("deploy bundle ended after %d of %d bytes: %w", written, metadata.Size, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	var extra [1]byte
	for {
		n, readErr := input.Read(extra[:])
		if n != 0 {
			return fmt.Errorf("deploy bundle exceeds declared size %d", metadata.Size)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("finish deploy bundle: %w", readErr)
		}
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != metadata.SHA256 {
		return fmt.Errorf("deploy bundle checksum mismatch")
	}
	if err := extract(bundlePath, destination); err != nil {
		return err
	}
	return os.Remove(bundlePath)
}

func validateMetadata(metadata Metadata) error {
	if metadata.Size <= 0 || metadata.Size > MaxBundleBytes {
		return fmt.Errorf("deploy bundle size must be between 1 and %d bytes", MaxBundleBytes)
	}
	if !sha256Re.MatchString(metadata.SHA256) {
		return fmt.Errorf("invalid deploy bundle sha256")
	}
	return nil
}

func extract(bundlePath, destination string) error {
	bundle, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer bundle.Close()

	seen := map[string]bool{}
	tr := tar.NewReader(bundle)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read deploy bundle: %w", err)
		}
		max, allowed := map[string]int64{SourceName: MaxBundleBytes, ManifestName: MaxManifestBytes}[header.Name]
		if !allowed || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > max {
			return fmt.Errorf("deploy bundle contains invalid entry %q", header.Name)
		}
		seen[header.Name] = true
		path := filepath.Join(destination, header.Name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	}
	if !seen[SourceName] || !seen[ManifestName] || len(seen) != 2 {
		return fmt.Errorf("deploy bundle must contain exactly %s and %s", SourceName, ManifestName)
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	n      int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.n += int64(n)
	return n, err
}
