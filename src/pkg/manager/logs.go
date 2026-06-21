package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ensureLogMigrated records the one-time legacy-log migration marker. The live
// tree was migrated long ago; this only guarantees the marker exists so the
// check stays cheap and never re-moves files.
func (m *Manager) ensureLogMigrated() {
	marker := filepath.Join(m.logDir(), ".migrated")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	_ = os.WriteFile(marker, nil, 0o644)
}

// newLogPath returns a unique log path for a process under
// output/logs/<name>/<year>/<month>/<name>_<timestamp>.log.
func (m *Manager) newLogPath(name string) string {
	m.ensureLogMigrated()
	now := time.Now()
	dir := filepath.Join(m.logDir(), name, now.Format("2006"), now.Format("01"))
	_ = os.MkdirAll(dir, 0o755)
	base := fmt.Sprintf("%s_%s.log", name, now.Format("060102_150405"))
	return ensureUniquePath(filepath.Join(dir, base))
}

// ensureUniquePath appends _N before the extension until the path is free.
func ensureUniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := path[:len(path)-len(ext)]
	for counter := 1; ; counter++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, counter, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// latestLogPath returns the most recent log file for a process, preferring the
// path recorded in state and falling back to the newest file on disk.
func (m *Manager) latestLogPath(name string) string {
	data := m.loadStateFile()
	if p, ok := data.Processes[name]; ok && p.LogPath != "" {
		if _, err := os.Stat(p.LogPath); err == nil {
			return p.LogPath
		}
	}
	root := filepath.Join(m.logDir(), name)
	var newest string
	var newestMod time.Time
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".log" {
			return nil
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = path, info.ModTime()
		}
		return nil
	})
	return newest
}

// sortedNames returns the process names in deterministic order.
func sortedNames(procs map[string]*Process) []string {
	names := make([]string, 0, len(procs))
	for name := range procs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
