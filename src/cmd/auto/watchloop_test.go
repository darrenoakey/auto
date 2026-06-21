package main

import (
	"syscall"
	"testing"

	"auto/pkg/manager"
)

func TestHandleWatchSignalInterruptStopsCleanly(t *testing.T) {
	m := manager.New(t.TempDir())
	if code := handleWatchSignal(m, syscall.SIGINT); code != 0 {
		t.Fatalf("SIGINT handler = %d, want 0", code)
	}
}

func TestRunTickSafelyRecoversFromEmptyState(t *testing.T) {
	// A tick over an empty temp-rooted manager must not panic.
	m := manager.New(t.TempDir())
	runTickSafely(m)
}
