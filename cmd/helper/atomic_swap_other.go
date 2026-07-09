//go:build !linux

package helper

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func exchangeDirs(left, right string) error {
	backup := filepath.Join(filepath.Dir(left), ".data-swap-old-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(left, backup); err != nil {
		return err
	}
	if err := os.Rename(right, left); err != nil {
		_ = os.Rename(backup, left)
		return err
	}
	if err := os.Rename(backup, right); err != nil {
		return err
	}
	return nil
}
