package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

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
	m.WaitForLogArchive()
}

func TestSignalReaderInhibitsBeforeMainLoopHandlesSignal(t *testing.T) {
	m := manager.New(t.TempDir())
	if err := m.AddProcess("svc", "sleep 300", nil, ""); err != nil {
		t.Fatal(err)
	}
	signals, stop := watchSignals(m)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-signals:
		if sig != syscall.SIGTERM {
			t.Fatalf("received %v", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM not delivered")
	}
	if _, err := m.StartProcess("svc"); !errors.Is(err, manager.ErrStartsInhibited) {
		t.Fatalf("launch while main loop has not handled signal: %v", err)
	}
}

func TestTermHandlerStopsRealServiceWithoutPersistingStop(t *testing.T) {
	m := manager.New(t.TempDir())
	if err := m.AddProcess("svc", "sleep 300", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartProcess("svc"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.ShutdownAll)
	if code := handleWatchSignal(m, syscall.SIGTERM); code != 0 {
		t.Fatalf("SIGTERM handler = %d", code)
	}
	if _, alive := m.Status("svc"); alive {
		t.Fatal("SIGTERM left service running")
	}
	if _, err := m.StartProcess("svc"); !errors.Is(err, manager.ErrStartsInhibited) {
		t.Fatalf("SIGTERM allowed restart: %v", err)
	}
}
