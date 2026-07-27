package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Process is one entry in the state file: both its definition (command, port,
// workdir) and its runtime state (pid, restart bookkeeping). Field names and
// nullability match the original Python state.json exactly so existing files
// load unchanged.
type Process struct {
	Command                string   `json:"command,omitempty"`
	Port                   *int     `json:"port,omitempty"`
	Workdir                string   `json:"workdir,omitempty"`
	Pid                    *int     `json:"pid,omitempty"`
	StartTime              *string  `json:"start_time,omitempty"`
	ExplicitlyStopped      bool     `json:"explicitly_stopped"`
	RestartAttempt         int      `json:"restart_attempt,omitempty"`
	LastRestartTime        *float64 `json:"last_restart_time,omitempty"`
	LogPath                string   `json:"log_path,omitempty"`
	RestartIntervalSeconds *int     `json:"restart_interval_seconds,omitempty"`
	LastPeriodicRestart    *float64 `json:"last_periodic_restart,omitempty"`
}

// stateFile is the top-level shape of state.json.
type stateFile struct {
	Processes map[string]*Process `json:"processes"`
}

// loadStateFresh reads the entire state file from disk, recovering from
// corruption via the .bak backup when possible. An empty or corrupt file with
// no valid backup is treated as fresh state rather than crashing the supervisor.
//
// It is the cache-miss path and the reader used by mutators (withState), which
// must always see the latest committed state. Read-only callers use
// loadStateFile, which returns a memoized snapshot of this result.
func (m *Manager) loadStateFresh() *stateFile {
	data, err := readStateJSON(m.statePath())
	if err == nil {
		return data
	}
	if os.IsNotExist(err) {
		// A missing state file is normal on first run, not corruption.
		return &stateFile{Processes: map[string]*Process{}}
	}
	backup := backupPath(m.statePath())
	if recovered, berr := readStateJSON(backup); berr == nil {
		fmt.Printf("Warning: %s corrupt (%v), restored from backup\n", m.statePath(), err)
		m.saveStateFile(recovered)
		return recovered
	}
	fmt.Printf("Warning: %s corrupt (%v), no valid backup, treating as fresh state\n", m.statePath(), err)
	return &stateFile{Processes: map[string]*Process{}}
}

// loadStateFile returns the current state for read-only callers, memoizing the
// parsed file so the watch loop's many per-service reads in a single tick do
// not each re-read and re-parse the whole file. With ~50 services the tick loop
// previously issued ~200 os.ReadFile+json.Unmarshal calls per second, which was
// the daemon's entire CPU footprint.
//
// The returned *stateFile is shared read-only memory: callers must not mutate
// it. Every mutation goes through withState -> loadStateFresh -> saveStateFile,
// and saveStateFile bumps stateCacheGen to invalidate this snapshot. A write by
// a concurrent CLI invocation (a separate process with its own Manager) is
// detected by a stat mtime/size change. The snapshot self-heals within one tick.
func (m *Manager) loadStateFile() *stateFile {
	path := m.statePath()
	st, stErr := os.Stat(path)
	m.stateCacheMu.Lock()
	if m.stateCache != nil && m.stateCacheSavedGen == m.stateCacheGen && stateFileUnchanged(st, stErr, m.stateCacheExists, m.stateCacheMtime, m.stateCacheSize) {
		cached := m.stateCache
		m.stateCacheMu.Unlock()
		return cached
	}
	m.stateCacheMu.Unlock()

	data := m.loadStateFresh()
	if data == nil {
		data = &stateFile{Processes: map[string]*Process{}}
	}

	// Re-stat after loadStateFresh: a corruption-recovery write (or a racing
	// external write) may have changed the file, and the cache must key on the
	// post-read mtime so it invalidates correctly on the next change.
	st2, _ := os.Stat(path)
	m.stateCacheMu.Lock()
	m.stateCache = data
	m.stateCacheMtime, m.stateCacheSize, m.stateCacheExists = cacheStatOf(st2, st)
	m.stateCacheSavedGen = m.stateCacheGen
	m.stateCacheMu.Unlock()
	return data
}

// stateFileUnchanged reports whether the on-disk state file still matches the
// cached snapshot described by (exists, mtime, size). A missing file matches
// only another missing file; any other stat error forces a re-read.
func stateFileUnchanged(st os.FileInfo, stErr error, cacheExists bool, cacheMtime time.Time, cacheSize int64) bool {
	if stErr != nil {
		return !cacheExists && os.IsNotExist(stErr)
	}
	if !cacheExists {
		return false
	}
	return st.Size() == cacheSize && st.ModTime().Equal(cacheMtime)
}

// cacheStatOf reduces one or two os.Stat results to the (mtime, size, exists)
// triple cached for invalidation, preferring the later stat when present.
func cacheStatOf(st2, st os.FileInfo) (time.Time, int64, bool) {
	if st2 != nil {
		return st2.ModTime(), st2.Size(), true
	}
	if st != nil {
		return st.ModTime(), st.Size(), true
	}
	return time.Time{}, 0, false
}

// readStateJSON parses a state file from disk, returning an error for missing,
// empty, or malformed content so the caller can fall back to the backup.
func readStateJSON(path string) (*stateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("empty file")
	}
	var data stateFile
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if data.Processes == nil {
		data.Processes = map[string]*Process{}
	}
	return &data, nil
}

// saveStateFile writes the state file atomically (unique temp + rename) and
// refreshes the .bak backup. A per-write unique temp avoids a rename race
// between concurrent auto invocations.
func (m *Manager) saveStateFile(data *stateFile) {
	path := m.statePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	_ = os.WriteFile(backupPath(path), payload, 0o644)
	m.stateCacheMu.Lock()
	m.stateCacheGen++
	m.stateCacheMu.Unlock()
}

// backupPath returns the .bak companion path for the state file.
func backupPath(statePath string) string {
	return strings.TrimSuffix(statePath, ".json") + ".json.bak"
}
