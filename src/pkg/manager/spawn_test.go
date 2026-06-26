package manager

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSpawnWithRetrySurvivor(t *testing.T) {
	m := newTestManager(t)
	pid, logPath, err := m.spawnWithRetry("svc", "sleep 300", "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !isProcessAlive(pid) {
		t.Fatalf("survivor pid %d should be alive", pid)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log path should exist: %v", err)
	}
}

func TestSpawnWithRetryFastExitHandedBack(t *testing.T) {
	m := newTestManager(t)
	pid, _, err := m.spawnWithRetry("svc", "true", "")
	if err != nil {
		t.Fatalf("fast-exit non-transient should be handed back without error, got %v", err)
	}
	if isProcessAlive(pid) {
		t.Fatalf("pid %d should have exited", pid)
	}
}

func TestLogHasTransientExecError(t *testing.T) {
	dir := t.TempDir()
	withMarker := filepath.Join(dir, "bad.log")
	if err := os.WriteFile(withMarker, []byte("zsh: Resource deadlock avoided\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !logHasTransientExecError(withMarker, 0) {
		t.Fatal("should detect transient marker")
	}
	clean := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(clean, []byte("listening on :8080\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if logHasTransientExecError(clean, 0) {
		t.Fatal("clean log should not match")
	}
}

func TestLogHasTransientExecErrorRespectsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "day.log")
	// A transient marker from an earlier spawn the same day ...
	stale := []byte("zsh: Resource deadlock avoided\n")
	if err := os.WriteFile(path, stale, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// ... followed by this spawn's clean output.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("listening on :8080\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	// Inspecting from the offset of this spawn should NOT see the stale marker.
	if logHasTransientExecError(path, int64(len(stale))) {
		t.Fatal("stale marker before offset should not be detected")
	}
	// But a full-file read (offset 0) still sees it.
	if !logHasTransientExecError(path, 0) {
		t.Fatal("full-file read should still detect the stale marker")
	}
}

func TestIsTransientSpawnError(t *testing.T) {
	if !isTransientSpawnError(syscall.EAGAIN) {
		t.Fatal("EAGAIN should be transient")
	}
	if isTransientSpawnError(syscall.ENOENT) {
		t.Fatal("ENOENT should not be transient")
	}
}
