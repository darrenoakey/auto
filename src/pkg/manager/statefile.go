package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// loadStateFile reads the entire state file, recovering from corruption via the
// .bak backup when possible. An empty or corrupt file with no valid backup is
// treated as fresh state rather than crashing the supervisor.
func (m *Manager) loadStateFile() *stateFile {
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
}

// backupPath returns the .bak companion path for the state file.
func backupPath(statePath string) string {
	return strings.TrimSuffix(statePath, ".json") + ".json.bak"
}
