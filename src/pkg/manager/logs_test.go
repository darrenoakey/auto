package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyLogPathSameDay(t *testing.T) {
	m := newTestManager(t)
	first := m.dailyLogPath("svc")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	second := m.dailyLogPath("svc")
	if first != second {
		t.Fatalf("spawns within the same day must share one log path;\n first = %s\n second = %s", first, second)
	}
	if !strings.Contains(first, filepath.Join("logs", "svc")) {
		t.Fatalf("log path %s not under logs/svc", first)
	}
	if !strings.HasSuffix(first, time.Now().Format("2006-01-02")+".log") {
		t.Fatalf("daily log path %s should end with the current date", first)
	}
}

func TestDailyLogPathAppends(t *testing.T) {
	m := newTestManager(t)
	path := m.dailyLogPath("svc")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "first\nsecond\n" {
		t.Fatalf("daily log should accumulate across spawns, got %q", got)
	}
}

func TestLatestLogPathPrefersRecorded(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	logPath := m.dailyLogPath("svc")
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
