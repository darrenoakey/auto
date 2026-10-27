package manager

import (
	"testing"
	"time"
)

// loseRecord reproduces a service record that lost track of its live child:
// the recorded pid names a process that no longer exists, exactly as the
// agentd-gauge record did while its real child kept serving its port.
func loseRecord(t *testing.T, m *Manager, name string, stopped bool) {
	t.Helper()
	m.mutateProcess(name, func(p *Process) {
		dead := 999999
		stale := "Mon Jan  1 00:00:00 2001"
		p.Pid = &dead
		p.StartTime = &stale
		p.ExplicitlyStopped = stopped
	})
	if _, alive := m.processStatus(name); alive {
		t.Fatal("record still names a live process")
	}
}

func TestWatchTickAdoptsItsOwnLiveChildInsteadOfRestarting(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "kept", "sleep 300", nil)
	pid, err := m.StartProcess("kept")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("kept", true) })
	logPath := m.loadStateFile().Processes["kept"].LogPath
	loseRecord(t, m, "kept", false)

	m.WatchTick()

	got, alive := m.Status("kept")
	if !alive || got != pid {
		t.Fatalf("Status = (%d,%v), want the original live child %d adopted", got, alive, pid)
	}
	p := m.loadStateFile().Processes["kept"]
	if p.LogPath != logPath || p.RestartAttempt != 0 {
		t.Fatalf("adopted record = log %q attempt %d, want log %q attempt 0", p.LogPath, p.RestartAttempt, logPath)
	}
	if strays := m.strayOwnChildren("kept", got); len(strays) != 0 {
		t.Fatalf("a second copy was started beside the adopted child: %v", strays)
	}
}

func TestWatchTickStopsTheStrayChildOfAStoppedService(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "halted", "sleep 300", nil)
	pid, err := m.StartProcess("halted")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("halted", true) })
	// The operator's stop landed while the child was starting: the record is
	// stopped and names no live process, but the child survived.
	loseRecord(t, m, "halted", true)

	m.WatchTick()

	deadline := time.Now().Add(5 * time.Second)
	for isProcessAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("stray child %d of a stopped service still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !m.isExplicitlyStopped("halted") {
		t.Fatal("recovery must not clear the operator's stop")
	}
}

func TestOwnChildRegistryNeverNamesAnExitedChild(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "brief", "sleep 300", nil)
	pid, err := m.StartProcess("brief")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.StopProcess("brief", true); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if strays := m.strayOwnChildren("brief", 0); len(strays) != 0 {
		t.Fatalf("exited child %d still registered: %v", pid, strays)
	}
	if _, ok := m.ownChildRecord(pid); ok {
		t.Fatalf("exited child %d was not pruned", pid)
	}
}
