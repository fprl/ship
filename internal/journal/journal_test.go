package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAppendAndReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "journal.jsonl")
	want := map[string]any{"event": "deployed", "sequence": float64(1)}

	if err := Append(path, want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	var got []map[string]any
	torn, err := Read(path, func(line []byte) error {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		got = append(got, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if torn {
		t.Fatal("Read() torn = true, want false")
	}
	if !reflect.DeepEqual(got, []map[string]any{want}) {
		t.Fatalf("records = %#v, want %#v", got, []map[string]any{want})
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("journal does not end in a commit delimiter: %q", raw)
	}
}

func TestAppendPreservesEarlierRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	for _, entry := range []map[string]string{{"event": "one"}, {"event": "two"}} {
		if err := Append(path, entry); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	if torn, err := Read(path, func(line []byte) error {
		var entry map[string]string
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}
		got = append(got, entry["event"])
		return nil
	}); err != nil || torn {
		t.Fatalf("Read() = torn %v, err %v", torn, err)
	}
	if !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestReadDiscardsOneTornTailExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("{\"event\":\"complete\"}\n{\"event\":"), 0644); err != nil {
		t.Fatal(err)
	}

	var lines [][]byte
	torn, err := Read(path, func(line []byte) error {
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !torn {
		t.Fatal("Read() torn = false, want true")
	}
	if len(lines) != 1 || string(lines[0]) != "{\"event\":\"complete\"}" {
		t.Fatalf("complete lines = %q", lines)
	}
}

func TestReadDiscardsValidJSONTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("{\"event\":\"complete\"}\n{\"event\":\"valid-but-torn\"}"), 0644); err != nil {
		t.Fatal(err)
	}

	count := 0
	torn, err := Read(path, func(line []byte) error {
		count++
		var value map[string]string
		return json.Unmarshal(line, &value)
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !torn || count != 1 {
		t.Fatalf("Read() = torn %v, callback count %d; want true, 1", torn, count)
	}
}

func TestReadMalformedTerminatedLineIncludesLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("{}\nnot-json\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path, func(line []byte) error {
		var value map[string]any
		return json.Unmarshal(line, &value)
	})
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error = %v, want line number", err)
	}
}

func TestReadEmptyAndMissingFiles(t *testing.T) {
	for _, name := range []string{"empty.jsonl", "missing.jsonl"} {
		path := filepath.Join(t.TempDir(), name)
		if name == "empty.jsonl" {
			if err := os.WriteFile(path, nil, 0644); err != nil {
				t.Fatal(err)
			}
		}
		called := false
		torn, err := Read(path, func([]byte) error {
			called = true
			return nil
		})
		if err != nil || torn || called {
			t.Fatalf("Read(%s) = torn %v, err %v, called %v", name, torn, err, called)
		}
	}
}

func TestReadCallbackErrorIncludesLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Read(path, func(line []byte) error {
		if string(line) == "two" {
			return os.ErrInvalid
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error = %v, want line number", err)
	}
}
