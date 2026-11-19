package manager

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

// termIgnoringCommand returns a real /bin/sh sleeper that traps and ignores
// SIGTERM, so grace timeouts are exercised against a genuinely
// signal-resistant live process. No mocks: the signal is really delivered.
func termIgnoringCommand(seconds int) string {
	return "/bin/sh -c \"trap '' TERM; sleep 600\""
}

// startChild adds and starts a real child, returning its pid.
func startChild(t *testing.T, m *Manager, name, command string) int {
	t.Helper()
	mustAdd(t, m, name, command, nil)
	pid, err := m.StartProcess(name)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() { killGroup(t, pid) })
	waitForProcessAlive(t, pid)
	return pid
}

func waitForProcessAlive(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if isProcessAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d never became alive", pid)
}

// advertiseTermOnlyDaemon writes a live heartbeat for pid WITH the
// FeatureTermOnly capability bit, as a daemon built from this code does every
// tick. Use a pid distinct from the test process to force real delegation.
func advertiseTermOnlyDaemon(t *testing.T, m *Manager, pid int) {
	t.Helper()
	m.withState(func(data *stateFile) bool {
		now := nowUnix()
		features := FeatureTermOnly
		data.DaemonHeartbeat = &now
		data.DaemonPid = &pid
		data.DaemonFeatures = &features
		return true
	})
}

// TestTermOnlyFailsClosedAgainstOldWatcher pins the pre-signal capability
// gate: with no heartbeat at all, or a live heartbeat that lacks the
// term-only feature bit (an older watcher), the restart must refuse BEFORE
// writing any hold state and BEFORE delivering any signal.
func TestTermOnlyFailsClosedAgainstOldWatcher(t *testing.T) {
	m := newTestManager(t)
	oldPid := startChild(t, m, "svc", termIgnoringCommand(600))

	// No daemon heartbeat at all: refuse.
	if _, err := m.restartProcessTermOnly("svc", time.Second); err == nil {
		t.Fatal("term-only restart must refuse when no daemon heartbeat exists")
	}
	// Live heartbeat WITHOUT the feature bit (an old watcher): refuse.
	setHeartbeat(t, m, syscall.Getpid(), 0)
	if _, err := m.restartProcessTermOnly("svc", time.Second); err == nil {
		t.Fatal("term-only restart must refuse against a watcher without term-only support")
	}
	def, _ := m.definition("svc")
	if def.TermStopRequestedAt != nil || def.TermStopTimedOut {
		t.Fatal("refusal must leave no hold state behind")
	}
	if !isProcessAlive(oldPid) {
		t.Fatal("refusal path must never signal the process")
	}
}

// TestRestartProcessTermOnlyGracefulPath: a TERM-honoring process exits within
// the grace, the replacement starts, the hold is fully released, and the new
// instance is supervised by a watch tick.
func TestRestartProcessTermOnlyGracefulPath(t *testing.T) {
	m := newTestManager(t)
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())
	oldPid := startChild(t, m, "svc", "/bin/sh -c \"sleep 600\"")

	pid, err := m.restartProcessTermOnly("svc", 10*time.Second)
	if err != nil {
		t.Fatalf("term-only restart: %v", err)
	}
	t.Cleanup(func() { killGroup(t, pid) })
	if pid == oldPid {
		t.Fatal("restart returned the old pid")
	}
	if isProcessAlive(oldPid) {
		t.Fatal("old instance survived a graceful term-only restart")
	}
	def, _ := m.definition("svc")
	if def.TermStopRequestedAt != nil || def.TermStopTimedOut {
		t.Fatal("completed restart left term-only state behind")
	}
	m.WatchTick()
	got, alive := m.processStatus("svc")
	if !alive || got != pid {
		t.Fatalf("Status = (%d, %v); want (%d, true)", got, alive, pid)
	}
}

// TestRestartProcessTermOnlyTimeoutLeavesProcessUntouched: after grace
// expiry against a TERM-ignoring process, no SIGKILL ever happens, no
// replacement spawns, and the durable marker refuses further term-only
// requests. The old process keeps running, untouched.
func TestRestartProcessTermOnlyTimeoutLeavesProcessUntouched(t *testing.T) {
	m := newTestManager(t)
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())
	oldPid := startChild(t, m, "svc", termIgnoringCommand(600))

	_, err := m.restartProcessTermOnly("svc", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if !isProcessAlive(oldPid) {
		t.Fatal("timed-out process must be left running and untouched")
	}
	def, _ := m.definition("svc")
	if !def.TermStopTimedOut {
		t.Fatal("timeout must record the durable marker")
	}
	if def.TermStopRequestedAt != nil {
		t.Fatal("timeout must clear the in-flight request marker")
	}
	// A second term-only request is refused while the marker persists.
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())
	if _, err := m.restartProcessTermOnly("svc", time.Second); err == nil {
		t.Fatal("second term-only request must be refused under a timed-out hold")
	}
	// The marker survives a fresh Manager (a restarted watcher).
	fresh := New(m.Root())
	fdef, _ := fresh.definition("svc")
	if !termStopHoldActive(fdef) {
		t.Fatal("timed-out marker must inhibit a fresh watcher")
	}
}

// TestTermStopTimedOutMarkerSurvivesOldProcessSelfExit is the race
// regression: the old worker finally exits on its own WHILE the timed-out
// marker persists, and another manager's watch ticks must not relaunch it —
// a late natural exit is not operator resolution.
func TestTermStopTimedOutMarkerSurvivesOldProcessSelfExit(t *testing.T) {
	m := newTestManager(t)
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())
	// Ignores TERM for 2s, then exits on its own.
	oldPid := startChild(t, m, "svc", "/bin/sh -c \"trap '' TERM; sleep 2\"")

	_, err := m.restartProcessTermOnly("svc", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if !isProcessAlive(oldPid) {
		t.Fatal("timed-out old process must still be running")
	}

	// The old process now exits on its own while the marker persists; the
	// watcher keeps ticking throughout and must never relaunch.
	watcher := New(m.Root())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && isProcessAlive(oldPid) {
		watcher.WatchTick()
		time.Sleep(50 * time.Millisecond)
	}
	waitForActualProcessDeath(t, oldPid)
	for i := 0; i < 5; i++ {
		watcher.WatchTick()
		time.Sleep(100 * time.Millisecond)
	}
	if _, alive := watcher.processStatus("svc"); alive {
		t.Fatal("watcher relaunched a service whose term-only restart timed out")
	}
	def, _ := watcher.definition("svc")
	if !def.TermStopTimedOut {
		t.Fatal("timeout marker must persist after old-process self-exit")
	}

	// Operator recovery: a completed stop clears the hold and a later start
	// is supervised normally.
	if err := watcher.StopProcess("svc", true); err != nil {
		t.Fatalf("operator stop: %v", err)
	}
	def, _ = watcher.definition("svc")
	if def.TermStopTimedOut || def.TermStopRequestedAt != nil {
		t.Fatal("completed operator stop must release the term-only hold")
	}
	advertiseTermOnlyDaemon(t, watcher, syscall.Getpid())
	pid, err := watcher.StartProcess("svc")
	if err != nil {
		t.Fatalf("operator start after stop: %v", err)
	}
	t.Cleanup(func() { killGroup(t, pid) })
	if _, alive := watcher.processStatus("svc"); !alive {
		t.Fatal("operator-started replacement must be supervised")
	}
}

// TestTermStopHoldBlocksWatchTickSupervision pins the durable inhibition and
// the gate-positive handoff: under an in-flight hold the watcher neither
// restarts nor adopts, but it MUST still fulfill the term-only restart's own
// delegated replacement-spawn request.
func TestTermStopHoldBlocksWatchTickSupervision(t *testing.T) {
	m := newTestManager(t)
	startChild(t, m, "svc", termIgnoringCommand(600))

	// In-flight request, written exactly as beginTermStop would.
	now := nowUnix()
	m.mutateProcess("svc", func(p *Process) {
		p.TermStopRequestedAt = &now
		p.TermStopOriginalPid = p.Pid
		p.TermStopOriginalStartTime = p.StartTime
	})
	watcher := New(m.Root())
	watcher.WatchTick()
	if _, alive := watcher.processStatus("svc"); !alive {
		t.Fatal("watch tick touched a held process")
	}

	// Delegated spawn must still be fulfilled under the hold (the handoff).
	oldPid, _ := m.processStatus("svc")
	killGroup(t, oldPid)
	waitForActualProcessDeath(t, oldPid)
	m.mutateProcess("svc", func(p *Process) {
		req := nowUnix()
		p.Pid = nil
		p.StartTime = nil
		p.TermStopRequestedAt = &req
		p.SpawnRequestedAt = &req
	})
	watcher.WatchTick()
	newPid, alive := watcher.processStatus("svc")
	if !alive {
		t.Fatal("watch tick must fulfill a delegated spawn request under a term-only hold")
	}
	t.Cleanup(func() { killGroup(t, newPid) })
	def, _ := m.definition("svc")
	if def.TermStopTimedOut {
		t.Fatal("successful replacement start must clear the timeout marker")
	}

	// Timeout marker with a DEAD entry: crash supervision must stay blocked.
	m.mutateProcess("svc", func(p *Process) {
		p.Pid = nil
		p.StartTime = nil
		p.TermStopRequestedAt = nil
		p.TermStopTimedOut = true
	})
	watcher.WatchTick()
	time.Sleep(200 * time.Millisecond)
	if _, alive := watcher.processStatus("svc"); alive {
		t.Fatal("watch tick crash-restarted an entry whose term-only hold timed out")
	}
}

// TestTermStopRequestTTLDrainsAbandonedRequest: an in-flight request whose
// requester vanished stops gating supervision once its TTL elapses.
func TestTermStopRequestTTLDrainsAbandonedRequest(t *testing.T) {
	m := newTestManager(t)
	startChild(t, m, "svc", termIgnoringCommand(600))
	expired := nowUnix() - (TermStopRequestTTL + 5*time.Second).Seconds()
	m.mutateProcess("svc", func(p *Process) {
		p.TermStopRequestedAt = &expired
		p.TermStopOriginalPid = p.Pid
		p.TermStopOriginalStartTime = p.StartTime
	})
	def, _ := m.definition("svc")
	if termStopHoldActive(def) {
		t.Fatal("expired term-only request must not hold supervision")
	}
	m.WatchTick()
	if _, alive := m.processStatus("svc"); !alive {
		t.Fatal("supervision must resume after an abandoned request drains")
	}
}

// TestRestartProcessTermOnlyRefusesDeadIsolateUnknown pins the entry guards.
func TestRestartProcessTermOnlyRefusesDeadIsolateUnknown(t *testing.T) {
	m := newTestManager(t)
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())
	mustAdd(t, m, "dead", "/bin/sh -c \"sleep 600\"", nil)
	if _, err := m.restartProcessTermOnly("dead", time.Second); err == nil {
		t.Fatal("term-only restart of a dead process must fail")
	}
	mustAdd(t, m, "iso", "/bin/sh -c \"sleep 600\"", nil)
	m.mutateProcess("iso", func(p *Process) { p.Isolate = true })
	if _, err := m.restartProcessTermOnly("iso", time.Second); err == nil {
		t.Fatal("term-only restart of an isolated process must fail")
	}
	if _, err := m.restartProcessTermOnly("missing", time.Second); err == nil {
		t.Fatal("term-only restart of an unknown process must fail")
	}
}

// TestRestartProcessTermOnlyOwnershipChangeWithholdsSignal proves the epoch
// check: if ownership of the entry changes between the request and the signal
// transaction (here: an operator stop completes), the SIGTERM is never sent
// and the restart fails with the ownership error.
func TestRestartProcessTermOnlyOwnershipChangeWithholdsSignal(t *testing.T) {
	m := newTestManager(t)
	oldPid := startChild(t, m, "svc", termIgnoringCommand(600))
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())

	if err := m.beginTermStop("svc", oldPid); err != nil {
		t.Fatalf("beginTermStop: %v", err)
	}
	// Operator stop wins the race: completed stop bumps the epoch and clears
	// the request fields — the signal transaction must refuse.
	pgid, _ := syscall.Getpgid(oldPid)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	waitForActualProcessDeath(t, oldPid)
	if err := m.StopProcess("svc", true); err != nil {
		t.Fatalf("operator stop: %v", err)
	}
	if err := m.signalTermStopVerified("svc", oldPid, pgid); err == nil {
		t.Fatal("signal must be withheld when ownership changed since the request")
	}
}

// TestRestartProcessTermOnlyReplacementWithLiveDaemon pins the end-to-end
// restart when the caller IS the live daemon (the common in-process test
// shape): TERM, graceful exit, replacement spawn, hold fully released, and
// the new pid recorded and supervised. Cross-process spawn delegation itself
// (SpawnRequestedAt handshake) is pinned by delegation_test.go and by the
// fulfillment branch of TestTermStopHoldBlocksWatchTickSupervision above.
func TestRestartProcessTermOnlyReplacementWithLiveDaemon(t *testing.T) {
	m := newTestManager(t)
	// Start BEFORE advertising the daemon so the first spawn is direct.
	oldPid := startChild(t, m, "svc", "/bin/sh -c \"sleep 600\"")
	advertiseTermOnlyDaemon(t, m, syscall.Getpid())

	pid, err := m.restartProcessTermOnly("svc", 10*time.Second)
	if err != nil {
		t.Fatalf("term-only restart: %v", err)
	}
	t.Cleanup(func() { killGroup(t, pid) })
	if pid == oldPid {
		t.Fatal("replacement pid equals old pid")
	}
	if isProcessAlive(oldPid) {
		t.Fatal("old instance survived the restart")
	}
	def, _ := m.definition("svc")
	if def.TermStopRequestedAt != nil || def.TermStopTimedOut || def.SpawnRequestedAt != nil {
		t.Fatal("completed restart left hold or request state behind")
	}
	m.WatchTick()
	got, alive := m.processStatus("svc")
	if !alive || got != pid {
		t.Fatalf("Status = (%d, %v); want (%d, true)", got, alive, pid)
	}
}
