package manager

import (
	"os"
	"testing"
	"time"
)

// setHeartbeat writes a daemon heartbeat directly into the state file, as
// recordHeartbeat would from inside a real WatchTick, so StartProcess's
// liveDaemon check can be exercised without spawning an actual second
// process to act as the daemon.
func setHeartbeat(t *testing.T, m *Manager, pid int, age time.Duration) {
	t.Helper()
	m.withState(func(data *stateFile) bool {
		ts := nowUnix() - age.Seconds()
		p := pid
		data.DaemonHeartbeat = &ts
		data.DaemonPid = &p
		return true
	})
}

// TestStartProcessSpawnsDirectlyWithoutDaemonHeartbeat pins the baseline: a
// Manager whose state file has never been ticked by any watch loop (every
// test Manager, and a freshly installed daemon before its first tick) must
// spawn synchronously in the calling process, exactly as before this change.
func TestStartProcessSpawnsDirectlyWithoutDaemonHeartbeat(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	pid, err := m.StartProcess("svc")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	if !isProcessAlive(pid) {
		t.Fatalf("pid %d should be alive after direct start", pid)
	}
	def, _ := m.definition("svc")
	if def.SpawnRequestedAt != nil {
		t.Fatal("a direct spawn must never leave a delegated spawn request behind")
	}
}

// TestStartProcessDelegatesToLiveDaemonHeartbeat is the regression test for
// the real incident: activity/agentd-gauge were fork/exec'd directly by an
// agent CLI's own `auto restart` invocation, so the children inherited the
// CLI's macOS responsible-process identity and XNU coalition instead of
// Auto.app's, corrupting Force Quit / memory-pressure readings. With a live
// daemon heartbeat recorded, StartProcess must defer the actual fork/exec to
// whichever process owns that heartbeat rather than performing it itself.
func TestStartProcessDelegatesToLiveDaemonHeartbeat(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	fakeDaemonPid := os.Getpid() + 987654 // guaranteed distinct from this test process
	setHeartbeat(t, m, fakeDaemonPid, 0)

	type result struct {
		pid int
		err error
	}
	done := make(chan result, 1)
	go func() {
		pid, err := m.StartProcess("svc")
		done <- result{pid, err}
	}()

	// Wait for delegation to register a spawn request rather than spawning
	// directly — proves StartProcess deferred to the "daemon" instead of
	// forking itself.
	deadline := time.Now().Add(2 * time.Second)
	for {
		def, ok := m.definition("svc")
		if ok && def.SpawnRequestedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("StartProcess never registered a delegated spawn request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, alive := m.Status("svc"); alive {
		t.Fatal("a delegated StartProcess must not spawn directly itself")
	}

	// Simulate the daemon's own tick fulfilling the request.
	if !m.fulfillSpawnRequest("svc") {
		t.Fatal("fulfillSpawnRequest should have performed the spawn")
	}
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("delegated StartProcess returned error: %v", r.err)
		}
		if !isProcessAlive(r.pid) {
			t.Fatalf("delegated StartProcess returned dead pid %d", r.pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delegated StartProcess did not return after the daemon fulfilled the request")
	}
}

// TestFulfillSpawnRequestDiscardsStaleRequest ensures an abandoned request
// (the requester gave up, or died, before the daemon reached it) is both
// refused and cleaned up rather than spawning a late, unwanted copy or
// leaving the flag stuck in state forever.
func TestFulfillSpawnRequestDiscardsStaleRequest(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	m.mutateProcess("svc", func(p *Process) {
		ts := nowUnix() - SpawnRequestTTL.Seconds() - 5
		p.SpawnRequestedAt = &ts
	})
	if m.fulfillSpawnRequest("svc") {
		t.Fatal("a stale spawn request must not be fulfilled")
	}
	if _, alive := m.Status("svc"); alive {
		t.Fatal("a stale spawn request must not spawn the process")
	}
	def, _ := m.definition("svc")
	if def.SpawnRequestedAt != nil {
		t.Fatal("a discarded stale request must be cleared, not left dangling")
	}
}

// TestFulfillSpawnRequestSkipsAlreadyAliveProcess covers the benign race
// where the process was already started (e.g. by a crash-restart) before the
// daemon reached a still-pending delegated request for it: no duplicate copy
// must be spawned, and the flag must still be cleared.
func TestFulfillSpawnRequestSkipsAlreadyAliveProcess(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	first, err := m.StartProcess("svc")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	m.mutateProcess("svc", func(p *Process) {
		ts := nowUnix()
		p.SpawnRequestedAt = &ts
	})
	if m.fulfillSpawnRequest("svc") {
		t.Fatal("fulfillSpawnRequest must not spawn a duplicate for an already-running process")
	}
	second, alive := m.Status("svc")
	if !alive || second != first {
		t.Fatalf("expected unchanged pid %d, got %d (alive=%v)", first, second, alive)
	}
	def, _ := m.definition("svc")
	if def.SpawnRequestedAt != nil {
		t.Fatal("a request satisfied by a race must still be cleared")
	}
}

// TestWatchTickRecordsHeartbeat confirms WatchTick writes a fresh,
// self-identified heartbeat that liveDaemon can observe — the mechanism
// StartProcess relies on to detect a live watch daemon for this state file.
func TestWatchTickRecordsHeartbeat(t *testing.T) {
	m := newTestManager(t)
	if pid := m.liveDaemon(); pid != 0 {
		t.Fatalf("a freshly created state file must have no daemon heartbeat, got pid %d", pid)
	}
	m.WatchTick()
	if pid := m.liveDaemon(); pid != os.Getpid() {
		t.Fatalf("WatchTick should record this test process's own pid as the live daemon, got %d", pid)
	}
}
