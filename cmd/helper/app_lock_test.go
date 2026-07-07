package helper

import (
	"testing"
	"time"
)

func TestAppEnvLockBlocksSameAppEnv(t *testing.T) {
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())

	first, err := acquireAppEnvLock("api", "production")
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan *appEnvLock, 1)
	errs := make(chan error, 1)
	go func() {
		second, err := acquireAppEnvLock("api", "production")
		if err != nil {
			errs <- err
			return
		}
		acquired <- second
	}()

	select {
	case second := <-acquired:
		_ = second.Release()
		_ = first.Release()
		t.Fatal("second lock acquired before first lock released")
	case err := <-errs:
		_ = first.Release()
		t.Fatal(err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case second := <-acquired:
		if err := second.Release(); err != nil {
			t.Fatal(err)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("second lock did not acquire after first lock released")
	}
}

func TestAppEnvLockAllowsDifferentEnv(t *testing.T) {
	t.Setenv("SHIP_LOCK_DIR", t.TempDir())

	first, err := acquireAppEnvLock("api", "production")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	second, err := acquireAppEnvLock("api", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
