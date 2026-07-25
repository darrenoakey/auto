package manager

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxLogArchivesPerWatchTick bounds how many old .log files one watch tick
	// may zip, so a multi-gigabyte backlog cannot stall crash supervision.
	MaxLogArchivesPerWatchTick = 40
)

// maybeArchiveOldLogs zips previous-day (and older) process logs in the
// background at most once at a time. Today's live daily log is never touched.
func (m *Manager) maybeArchiveOldLogs() {
	m.logArchiveMu.Lock()
	if m.logArchiveBusy {
		m.logArchiveMu.Unlock()
		return
	}
	m.logArchiveBusy = true
	m.logArchiveMu.Unlock()
	go m.runLogArchivePass()
}

// runLogArchivePass drains a budgeted batch of old logs, then clears the busy
// flag so a later tick can continue the backlog.
func (m *Manager) runLogArchivePass() {
	defer func() {
		m.logArchiveMu.Lock()
		m.logArchiveBusy = false
		m.logArchiveMu.Unlock()
	}()
	if n := m.archiveOldLogs(MaxLogArchivesPerWatchTick); n > 0 {
		fmt.Printf("Archived %d old log file(s) to zip\n", n)
	}
}

// archiveOldLogs zips up to limit eligible .log files under the log tree.
// limit <= 0 means no budget (archive every eligible file). Returns how many
// files were archived. Safe to call concurrently with process supervision:
// today's active daily log is skipped by date.
func (m *Manager) archiveOldLogs(limit int) int {
	today := startOfLocalDay(time.Now())
	archived := 0
	_ = filepath.WalkDir(m.logDir(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if limit > 0 && archived >= limit {
			return fs.SkipAll
		}
		if !isPlainLogFile(path, entry) {
			return nil
		}
		if !shouldArchiveLog(path, today) {
			return nil
		}
		if err := archiveLogFile(path); err != nil {
			fmt.Printf("Failed to archive log %s: %v\n", path, err)
			return nil
		}
		archived++
		return nil
	})
	return archived
}

// isPlainLogFile reports whether entry is a regular *.log file (not a .log.zip).
func isPlainLogFile(path string, entry fs.DirEntry) bool {
	if !entry.Type().IsRegular() {
		return false
	}
	name := entry.Name()
	return strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.zip")
}

// shouldArchiveLog reports whether path is safe to zip: daily-named logs whose
// date is before today, or legacy logs last written before today.
func shouldArchiveLog(path string, today time.Time) bool {
	if day, ok := parseDailyLogDate(filepath.Base(path)); ok {
		return day.Before(today)
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.ModTime().Before(today)
}

// parseDailyLogDate extracts the calendar day from <name>_YYYY-MM-DD.log.
func parseDailyLogDate(baseName string) (time.Time, bool) {
	if !strings.HasSuffix(baseName, ".log") {
		return time.Time{}, false
	}
	stem := strings.TrimSuffix(baseName, ".log")
	underscore := strings.LastIndex(stem, "_")
	if underscore < 0 || underscore+1 >= len(stem) {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", stem[underscore+1:], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// startOfLocalDay returns local midnight for the calendar day of t.
func startOfLocalDay(t time.Time) time.Time {
	local := t.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
}

// archiveLogFile writes path+".zip" containing the log, then removes the plain
// log. If a good zip already exists, only the plain log is removed (recovery
// from a previous partial run).
func archiveLogFile(path string) error {
	zipPath := path + ".zip"
	if zipReady(zipPath) {
		return os.Remove(path)
	}
	if err := writeLogZipAtomic(path, zipPath); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing archived log: %w", err)
	}
	return nil
}

// zipReady reports whether path is a non-empty zip file.
func zipReady(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// writeLogZipAtomic builds the zip beside the log via a unique temp file and
// renames into place so readers never see a truncated archive.
func writeLogZipAtomic(logPath, zipPath string) error {
	tempPath := fmt.Sprintf("%s.%d.tmp", zipPath, time.Now().UnixNano())
	if err := writeLogZip(logPath, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, zipPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("publishing zip: %w", err)
	}
	return nil
}

// writeLogZip creates a single-entry zip at destPath holding the log's bytes
// under its base name.
func writeLogZip(logPath, destPath string) error {
	source, info, err := openLogSource(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := copyLogIntoZip(dest, source, info, filepath.Base(logPath)); err != nil {
		_ = dest.Close()
		return err
	}
	return dest.Close()
}

// openLogSource opens a log file and returns its handle plus FileInfo.
func openLogSource(logPath string) (*os.File, os.FileInfo, error) {
	source, err := os.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return nil, nil, err
	}
	return source, info, nil
}

// copyLogIntoZip writes one deflated zip entry named entryName from source.
func copyLogIntoZip(dest *os.File, source *os.File, info os.FileInfo, entryName string) error {
	zipWriter := zip.NewWriter(dest)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		_ = zipWriter.Close()
		return err
	}
	header.Name = entryName
	header.Method = zip.Deflate
	entry, err := zipWriter.CreateHeader(header)
	if err != nil {
		_ = zipWriter.Close()
		return err
	}
	if _, err := io.Copy(entry, source); err != nil {
		_ = zipWriter.Close()
		return err
	}
	return zipWriter.Close()
}
