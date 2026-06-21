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
	if !logHasTransientExecError(withMarker) {
		t.Fatal("should detect transient marker")
	}
	clean := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(clean, []byte("listening on :8080\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if logHasTransientExecError(clean) {
		t.Fatal("clean log should not match")
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
