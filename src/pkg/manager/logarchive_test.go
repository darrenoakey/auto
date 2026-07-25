package manager

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveOldLogsZipsPreviousDayAndLeavesToday(t *testing.T) {
	m := newTestManager(t)
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	todayPath := writeNamedLog(t, m, "svc", today, "today-body\n")
	oldPath := writeNamedLog(t, m, "svc", yesterday, "old-body\n")

	if got := m.archiveOldLogs(0); got != 1 {
		t.Fatalf("archiveOldLogs = %d, want 1", got)
	}
	if _, err := os.Stat(todayPath); err != nil {
		t.Fatalf("today's log must remain: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("yesterday's plain log should be removed, stat err=%v", err)
	}
	zipPath := oldPath + ".zip"
	assertZipContains(t, zipPath, filepath.Base(oldPath), "old-body\n")
}

func TestArchiveOldLogsIsIdempotentWhenZipExists(t *testing.T) {
	m := newTestManager(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	oldPath := writeNamedLog(t, m, "svc", yesterday, "payload\n")
	if got := m.archiveOldLogs(0); got != 1 {
		t.Fatalf("first archiveOldLogs = %d, want 1", got)
	}
	if got := m.archiveOldLogs(0); got != 0 {
		t.Fatalf("second archiveOldLogs = %d, want 0", got)
	}
	assertZipContains(t, oldPath+".zip", filepath.Base(oldPath), "payload\n")
}

func TestArchiveOldLogsRespectsBudget(t *testing.T) {
	m := newTestManager(t)
	dayA := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	dayB := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	pathA := writeNamedLog(t, m, "svc", dayA, "a\n")
	pathB := writeNamedLog(t, m, "svc", dayB, "b\n")

	if got := m.archiveOldLogs(1); got != 1 {
		t.Fatalf("budgeted archiveOldLogs = %d, want 1", got)
	}
	plainLeft := 0
	for _, path := range []string{pathA, pathB} {
		if _, err := os.Stat(path); err == nil {
			plainLeft++
		}
	}
	if plainLeft != 1 {
		t.Fatalf("exactly one plain log should remain after budget 1, left=%d", plainLeft)
	}
	if got := m.archiveOldLogs(1); got != 1 {
		t.Fatalf("second budgeted pass = %d, want 1", got)
	}
	for _, path := range []string{pathA, pathB} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed after draining budget, err=%v", path, err)
		}
		assertZipContains(t, path+".zip", filepath.Base(path), "")
	}
}

func TestArchiveOldLogsZipsLegacyTimestampedLogs(t *testing.T) {
	m := newTestManager(t)
	dir := filepath.Join(m.logDir(), "legacy", "2026", "03")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "legacy_260325_222244.log")
	if err := os.WriteFile(path, []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got := m.archiveOldLogs(0); got != 1 {
		t.Fatalf("archiveOldLogs = %d, want 1", got)
	}
	assertZipContains(t, path+".zip", filepath.Base(path), "legacy\n")
}

func TestParseDailyLogDate(t *testing.T) {
	day, ok := parseDailyLogDate("svc_2026-07-25.log")
	if !ok {
		t.Fatal("expected parse ok")
	}
	want := time.Date(2026, 7, 25, 0, 0, 0, 0, time.Local)
	if !day.Equal(want) {
		t.Fatalf("day = %v, want %v", day, want)
	}
	if _, ok := parseDailyLogDate("legacy_260325_222244.log"); ok {
		t.Fatal("legacy timestamped name must not parse as daily date")
	}
}

func TestArchiveLogFileRecoversFromExistingZip(t *testing.T) {
	m := newTestManager(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	path := writeNamedLog(t, m, "svc", yesterday, "body\n")
	zipPath := path + ".zip"
	if err := writeLogZipAtomic(path, zipPath); err != nil {
		t.Fatalf("seed zip: %v", err)
	}
	// Leave the plain log in place to simulate a crash between zip and remove.
	if err := os.WriteFile(path, []byte("body\n"), 0o644); err != nil {
		t.Fatalf("rewrite plain: %v", err)
	}
	if err := archiveLogFile(path); err != nil {
		t.Fatalf("archiveLogFile recovery: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plain log should be gone after recovery, err=%v", err)
	}
	assertZipContains(t, zipPath, filepath.Base(path), "body\n")
}

func TestWatchTickArchivesOldLogs(t *testing.T) {
	m := newTestManager(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	oldPath := writeNamedLog(t, m, "svc", yesterday, "tick-body\n")
	m.WatchTick()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			assertZipContains(t, oldPath+".zip", filepath.Base(oldPath), "tick-body\n")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watch archive pass did not zip %s in time", oldPath)
}

// writeNamedLog creates a daily-named log with the given date suffix and body.
func writeNamedLog(t *testing.T, m *Manager, name, day, body string) string {
	t.Helper()
	parts := strings.Split(day, "-")
	if len(parts) != 3 {
		t.Fatalf("bad day %q", day)
	}
	dir := filepath.Join(m.logDir(), name, parts[0], parts[1])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%s.log", name, day))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// assertZipContains checks that zipPath is a readable zip with entryName.
// When wantBody is non-empty the entry bytes must match exactly.
func assertZipContains(t *testing.T, zipPath, entryName, wantBody string) {
	t.Helper()
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip %s: %v", zipPath, err)
	}
	defer func() { _ = reader.Close() }()
	if len(reader.File) != 1 {
		t.Fatalf("zip %s has %d entries, want 1", zipPath, len(reader.File))
	}
	file := reader.File[0]
	if file.Name != entryName {
		t.Fatalf("zip entry name = %q, want %q", file.Name, entryName)
	}
	if wantBody == "" {
		return
	}
	rc, err := file.Open()
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if !bytes.Equal(got, []byte(wantBody)) {
		t.Fatalf("zip body = %q, want %q", got, wantBody)
	}
}
