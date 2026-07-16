// Package journal provides the shared crash semantics for append-only JSONL
// journals.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Append writes one committed JSON record and waits for it to reach the file
// system before returning successfully.
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

	closeFile := func() error {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close journal: %w", closeErr)
		}
		return nil
	}
	closeOnError := func(operation string, operationErr error) error {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(
				fmt.Errorf("%s: %w", operation, operationErr),
				fmt.Errorf("close journal: %w", closeErr),
			)
		}
		return fmt.Errorf("%s: %w", operation, operationErr)
	}
	if err := truncateTornTail(file); err != nil {
		return closeOnError("repair journal tail", err)
	}
	n, err := file.Write(data)
	if err != nil {
		return closeOnError("write journal", err)
	}
	if n != len(data) {
		return closeOnError("write journal", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return closeOnError("sync journal", err)
	}
	return closeFile()
}

// truncateTornTail removes the uncommitted final record before appending.
// O_APPEND would otherwise concatenate the next record onto that tail and
// turn a recoverable crash into a permanently malformed terminated line.
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

// Read visits every committed record in order. An unterminated final segment
// is deliberately not decoded: it is a torn write, even if it is valid JSON.
func Read(path string, decode func([]byte) error) (torn bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open journal: %w", err)
	}

	reader := bufio.NewReader(file)
	lineNumber := 0
	closeOnError := func(operation string, operationErr error) error {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(
				fmt.Errorf("%s: %w", operation, operationErr),
				fmt.Errorf("close journal: %w", closeErr),
			)
		}
		return fmt.Errorf("%s: %w", operation, operationErr)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 && !strings.HasSuffix(line, "\n") {
			if readErr != nil && readErr != io.EOF {
				return false, closeOnError("read journal", readErr)
			}
			closeErr := file.Close()
			if closeErr != nil {
				return false, fmt.Errorf("close journal: %w", closeErr)
			}
			return true, nil
		}
		if len(line) > 0 {
			lineNumber++
			if err := decode([]byte(line[:len(line)-1])); err != nil {
				return false, closeOnError(fmt.Sprintf("decode journal line %d", lineNumber), err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, closeOnError("read journal", readErr)
		}
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close journal: %w", err)
	}
	return false, nil
}
