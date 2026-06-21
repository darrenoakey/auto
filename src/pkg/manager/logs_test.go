package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLogPathUnique(t *testing.T) {
	m := newTestManager(t)
	first := m.newLogPath("svc")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	second := m.newLogPath("svc")
	if first == second {
		t.Fatalf("expected unique paths, both = %s", first)
	}
	if !strings.Contains(first, filepath.Join("logs", "svc")) {
		t.Fatalf("log path %s not under logs/svc", first)
	}
}

func TestEnsureUniquePathAppendsSuffix(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "a.log")
	if got := ensureUniquePath(base); got != base {
		t.Fatalf("non-existent path should be returned as-is, got %s", got)
	}
	if err := os.WriteFile(base, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := ensureUniquePath(base)
	if got != filepath.Join(dir, "a_1.log") {
		t.Fatalf("ensureUniquePath = %s, want a_1.log", got)
	}
}

func TestLatestLogPathPrefersRecorded(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	logPath := m.newLogPath("svc")
	if err := os.WriteFile(logPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	setRuntime(t, m, "svc", func(p *Process) { p.LogPath = logPath })
	if got := m.latestLogPath("svc"); got != logPath {
		t.Fatalf("latestLogPath = %s, want %s", got, logPath)
	}
}

func TestLatestLogPathEmptyWhenNone(t *testing.T) {
	m := newTestManager(t)
	if got := m.latestLogPath("nope"); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}
