package manager

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
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

// TestWatchTickDoesNotRewalkIdleLogTree pins the CPU fix: a pass that finds no
// backlog must not be repeated on the very next one-second tick. Walking the
// log tree costs a full filepath.WalkDir (~76k files on a live box), so an
// unconditional per-tick pass burned ~28% of a core to discover nothing to do.
func TestWatchTickDoesNotRewalkIdleLogTree(t *testing.T) {
	m := newTestManager(t)
	// An idle tree: only today's live log, which is never archivable.
	writeNamedLog(t, m, "svc", time.Now().Format("2006-01-02"), "live\n")

	m.WatchTick()
	waitForArchivePass(t, m)
	first := m.logArchiveLastForTest()
	if first.IsZero() {
		t.Fatal("first watch tick must run an archive pass")
	}

	for i := 0; i < 5; i++ {
		m.WatchTick()
	}
	waitForArchivePass(t, m)
	if got := m.logArchiveLastForTest(); !got.Equal(first) {
		t.Fatalf("idle log tree was re-walked by a later tick: last pass moved %v -> %v", first, got)
	}
}

// TestLogArchiveDueRunsAgainOnBacklogDayRollAndInterval pins the three
// conditions that must still trigger a pass, so rate-limiting cannot strand a
// backlog or miss the day boundary where new logs become archivable.
func TestLogArchiveDueRunsAgainOnBacklogDayRollAndInterval(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		last     time.Time
		backlog  bool
		wantDue  bool
		checkNow time.Time
	}{
		{"never run", time.Time{}, false, true, now},
		{"just ran, idle", now, false, false, now},
		{"budget exhausted", now, true, true, now},
		{"day rolled", now.AddDate(0, 0, -1), false, true, now},
		{"interval elapsed", now.Add(-LogArchiveInterval), false, true, now},
		{"inside interval", now.Add(-LogArchiveInterval / 2), false, false, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t)
			m.logArchiveLast = tc.last
			m.logArchiveBacklog = tc.backlog
			if got := m.logArchiveDueLocked(tc.checkNow); got != tc.wantDue {
				t.Fatalf("logArchiveDueLocked = %v, want %v", got, tc.wantDue)
			}
		})
	}
}

// TestLogArchiveBacklogDrainsAcrossTicks pins that a budget-exhausting pass
// keeps running on subsequent ticks until the backlog is gone, and only then
// settles into the rate-limited idle state.
func TestLogArchiveBacklogDrainsAcrossTicks(t *testing.T) {
	m := newTestManager(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	total := MaxLogArchivesPerWatchTick + 5
	for i := 0; i < total; i++ {
		writeNamedLog(t, m, fmt.Sprintf("svc%03d", i), yesterday, "body\n")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m.WatchTick()
		waitForArchivePass(t, m)
		if countPlainLogs(t, m.logDir()) == 0 {
			break
		}
	}
	if n := countPlainLogs(t, m.logDir()); n != 0 {
		t.Fatalf("backlog did not drain across ticks: %d plain logs left", n)
	}

	m.logArchiveMu.Lock()
	backlog := m.logArchiveBacklog
	m.logArchiveMu.Unlock()
	if backlog {
		t.Fatal("drained backlog must clear logArchiveBacklog so the tree stops being re-walked")
	}
}

// logArchiveLastForTest returns when the last archive pass completed.
func (m *Manager) logArchiveLastForTest() time.Time {
	m.logArchiveMu.Lock()
	defer m.logArchiveMu.Unlock()
	return m.logArchiveLast
}

// waitForArchivePass blocks until no archive pass is in flight.
func waitForArchivePass(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.logArchiveMu.Lock()
		busy := m.logArchiveBusy
		m.logArchiveMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("archive pass did not finish in time")
}

// countPlainLogs returns how many unarchived *.log files remain under dir.
func countPlainLogs(t *testing.T, dir string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil //nolint:nilerr // a partially built tree is not a test failure
		}
		if isPlainLogFile(path, entry) {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return count
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
