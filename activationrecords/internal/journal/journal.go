package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fprl/ship/internal/identity"
)

func DeployPath(app, env string) string { return identity.DeployJournalFile(app, env) }

func LegacyDeployPath(app, env string) string { return identity.LegacyDeployJournalFile(app, env) }

func Append(path string, entry any) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal journal entry: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := truncateTornTail(file); err != nil {
		return fmt.Errorf("repair journal tail: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

func truncateTornTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	last := []byte{0}
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	const chunkSize int64 = 4096
	for end := info.Size(); end > 0; {
		start := end - chunkSize
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, end-start)
		if _, err := file.ReadAt(chunk, start); err != nil {
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				return file.Truncate(start + int64(i) + 1)
			}
		}
		end = start
	}
	return file.Truncate(0)
}

func Read(path string, decode func([]byte) error) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open journal: %w", err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 && !strings.HasSuffix(line, "\n") {
			if readErr != nil && readErr != io.EOF {
				return false, fmt.Errorf("read journal: %w", readErr)
			}
			return true, nil
		}
		if len(line) > 0 {
			lineNumber++
			if err := decode([]byte(line[:len(line)-1])); err != nil {
				return false, fmt.Errorf("decode journal line %d: %w", lineNumber, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, fmt.Errorf("read journal: %w", readErr)
		}
	}
	return false, nil
}
