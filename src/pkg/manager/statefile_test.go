package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// seedState writes raw JSON to the manager's state path, creating the directory.
func seedState(t *testing.T, m *Manager, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.statePath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(m.statePath(), []byte(content), 0o644); err != nil {
		t.Fatalf("seeding state: %v", err)
	}
}

// liveStateSample mirrors the production state.json field shapes: pid as int or
// null, last_restart_time as float or null, optional port/workdir, and a
// periodic-restart entry. Loading it must not lose or corrupt any field.
const liveStateSample = `{
  "processes": {
    "with-port": {
      "command": "python server.py",
      "port": 8080,
      "pid": 1234,
      "start_time": "Wed Jun 18 11:33:09 2026",
      "explicitly_stopped": false,
      "restart_attempt": 0,
      "last_restart_time": null,
      "log_path": "/tmp/with-port.log"
    },
    "with-workdir": {
      "command": "./serve",
      "workdir": "/tmp",
      "pid": null,
      "start_time": null,
      "explicitly_stopped": true,
      "restart_attempt": 3,
      "last_restart_time": 1750000000.5,
      "log_path": "/tmp/wd.log"
    },
    "periodic": {
      "command": "run-job",
      "pid": 42,
      "explicitly_stopped": false,
      "restart_interval_seconds": 86400,
      "last_periodic_restart": 1750000123.0
    }
  }
}`

func TestLoadStateFileParsesLiveFormat(t *testing.T) {
	m := newTestManager(t)
	seedState(t, m, liveStateSample)
	data := m.loadStateFile()
	if len(data.Processes) != 3 {
		t.Fatalf("got %d processes, want 3", len(data.Processes))
	}
	wp := data.Processes["with-port"]
	if wp.Port == nil || *wp.Port != 8080 || wp.Pid == nil || *wp.Pid != 1234 {
		t.Fatalf("with-port fields wrong: %+v", wp)
	}
	if wp.LastRestartTime != nil {
		t.Fatalf("with-port last_restart_time should be nil, got %v", *wp.LastRestartTime)
	}
	wd := data.Processes["with-workdir"]
	if wd.Pid != nil || wd.StartTime != nil || !wd.ExplicitlyStopped || wd.RestartAttempt != 3 {
		t.Fatalf("with-workdir fields wrong: %+v", wd)
	}
	if wd.LastRestartTime == nil || *wd.LastRestartTime != 1750000000.5 {
		t.Fatalf("with-workdir last_restart_time wrong: %v", wd.LastRestartTime)
	}
	pr := data.Processes["periodic"]
	if pr.RestartIntervalSeconds == nil || *pr.RestartIntervalSeconds != 86400 {
		t.Fatalf("periodic interval wrong: %+v", pr)
	}
}

func TestSaveStateFileRoundTrips(t *testing.T) {
	m := newTestManager(t)
	seedState(t, m, liveStateSample)
	before := m.loadStateFile()
	m.saveStateFile(before)
	after := m.loadStateFile()
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("round trip changed state:\n%s\n%s", beforeJSON, afterJSON)
	}
}

func TestSaveStateFileWritesBackup(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if _, err := os.Stat(backupPath(m.statePath())); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
}

func TestLoadStateFileRecoversFromBackup(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	// Corrupt the primary; the good backup must be restored.
	if err := os.WriteFile(m.statePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupting: %v", err)
	}
	data := m.loadStateFile()
	if _, ok := data.Processes["svc"]; !ok {
		t.Fatalf("did not recover svc from backup: %+v", data.Processes)
	}
}

func TestLoadStateFileFreshWhenMissing(t *testing.T) {
	m := newTestManager(t)
	data := m.loadStateFile()
	if data.Processes == nil || len(data.Processes) != 0 {
		t.Fatalf("expected empty fresh state, got %+v", data.Processes)
	}
}
