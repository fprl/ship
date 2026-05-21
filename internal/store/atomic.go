package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const jsonIndent = "  "

func readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("invalid %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", jsonIndent)
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), mode)
}

func writeHostFile(path string, file HostFile) error {
	file.Version = CurrentVersion
	normalizeHostFile(&file)
	if err := validateHostDesired(file.Desired); err != nil {
		return fmt.Errorf("invalid host desired: %w", err)
	}
	return writeJSON(path, file, 0644)
}

// writeHostState preserves desired as raw JSON so apply cannot rewrite intent
// while recording observed host state and apply metadata.
func writeHostState(path string, desired json.RawMessage, observed HostObserved, meta HostMeta) error {
	observedData, err := marshalHostStateField(observed)
	if err != nil {
		return err
	}
	metaData, err := marshalHostStateField(meta)
	if err != nil {
		return err
	}

	var out bytes.Buffer
	out.WriteString("{\n")
	writeHostStateField(&out, "version", []byte(strconv.Itoa(CurrentVersion)), true)
	writeHostStateField(&out, "desired", bytes.TrimSpace(desired), true)
	writeHostStateField(&out, "observed", observedData, true)
	writeHostStateField(&out, "meta", metaData, false)
	out.WriteString("}\n")
	return AtomicWrite(path, out.Bytes(), 0644)
}

func marshalHostStateField(value any) ([]byte, error) {
	return json.MarshalIndent(value, jsonIndent, jsonIndent)
}

func writeHostStateField(out *bytes.Buffer, name string, value []byte, comma bool) {
	out.WriteString(jsonIndent)
	out.Write(mustMarshalJSONString(name))
	out.WriteString(": ")
	out.Write(value)
	if comma {
		out.WriteByte(',')
	}
	out.WriteByte('\n')
}

func mustMarshalJSONString(value string) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal JSON object key: %v", err))
	}
	return data
}

func AtomicWrite(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
