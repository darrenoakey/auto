package manager

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWatchTickDoesNotRaceAConcurrentStart pins the double-spawn found by the
// canary probe: `auto add` (a separate CLI process) starts a service, and for
// the whole spawn window the entry still reads Pid=nil. A watch tick landing in
// that window used to see "dead, not explicitly stopped" and start a competing
// second copy, orphaning the first. The start claim closes that window.
func TestWatchTickDoesNotRaceAConcurrentStart(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "racer", "sleep 300", nil)
	t.Cleanup(func() { _ = m.StopProcess("racer", true) })

	// Simulate the claiming process being mid-spawn: claimed, no pid yet.
	if !m.claimStart("racer") {
		t.Fatal("first claim must succeed")
	}

	if m.shouldRestart("racer") {
		t.Fatal("watch loop must not restart a process whose start is in flight")
	}
	m.WatchTick()
	if _, alive := m.processStatus("racer"); alive {
		t.Fatal("watch tick started a competing copy while a start was in flight")
	}

	// A second starter must be refused rather than spawning a duplicate.
	if _, err := m.StartProcess("racer"); err == nil {
		t.Fatal("StartProcess must refuse to start a process that is already starting")
	}

	// Once the claim clears, supervision resumes normally.
	m.clearStartClaim("racer")
	if !m.shouldRestart("racer") {
		t.Fatal("supervision must resume once the start claim is released")
	}
}

// TestStartClaimExpiresSoAStuckClaimCannotWedgeSupervision pins the TTL: a
// claimant that dies mid-spawn (a killed CLI, or the daemon torn down by
// `auto install`) leaves a claim behind, and that must never suppress
// supervision permanently.
func TestStartClaimExpiresSoAStuckClaimCannotWedgeSupervision(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "stuck", "sleep 300", nil)

	stale := nowUnix() - StartInFlightTTL.Seconds() - 1
	m.mutateProcess("stuck", func(p *Process) { p.StartingSince = &stale })

	if !m.shouldRestart("stuck") {
		t.Fatal("an expired start claim must not keep suppressing supervision")
	}
	if !m.claimStart("stuck") {
		t.Fatal("an expired claim must be reclaimable")
	}
}

// TestRecordStartedClearsTheStartClaim pins that a successful start releases
// its claim, so the very next tick supervises the process normally.
func TestRecordStartedClearsTheStartClaim(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "clears", "sleep 300", nil)
	if _, err := m.StartProcess("clears"); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("clears", true) })

	data := m.loadStateFile()
	if p := data.Processes["clears"]; p == nil || p.StartingSince != nil {
		t.Fatal("a successful start must clear its claim")
	}
	if _, alive := m.processStatus("clears"); !alive {
		t.Fatal("started process must read as alive")
	}
}

// TestFailedStartReleasesTheClaim pins that a start which cannot spawn does not
// strand a claim, so the watch loop can retry the process.
func TestFailedStartReleasesTheClaim(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "doomed", "sleep 300", nil)
	// Point the entry at a workdir that no longer exists so the spawn itself
	// fails. Workdir is validated at add time, so it must be corrupted here to
	// reach StartProcess's post-claim failure path.
	m.mutateProcess("doomed", func(p *Process) { p.Workdir = "/nonexistent/workdir/for/test" })

	if _, err := m.StartProcess("doomed"); err == nil {
		_ = m.StopProcess("doomed", true)
		t.Fatal("start into a nonexistent workdir must fail")
	}
	data := m.loadStateFile()
	if p := data.Processes["doomed"]; p != nil && startClaimIsLive(p) {
		t.Fatal("a failed start must release its claim so the watch loop can retry")
	}
	// The watch loop must be free to try again immediately.
	if !m.shouldRestart("doomed") {
		t.Fatal("supervision must resume after a failed start releases its claim")
	}
}

// TestConcurrentStartAndWatchTickSpawnExactlyOnce drives the real race: a start
// and watch ticks run concurrently, exactly as the CLI and the daemon do. Only
// one copy may ever exist.
func TestConcurrentStartAndWatchTickSpawnExactlyOnce(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "concurrent", "sleep 31427", nil)
	t.Cleanup(func() { _ = m.StopProcess("concurrent", true) })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = m.StartProcess("concurrent")
	}()
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			m.WatchTick()
		}
	}()
	wg.Wait()

	pid, alive := m.processStatus("concurrent")
	if !alive {
		t.Fatal("process must be running after the race")
	}
	if n := countSleepers(t, "sleep 31427"); n != 1 {
		t.Fatalf("expected exactly 1 copy after concurrent start + watch, got %d (recorded pid %d)", n, pid)
	}
}

// countSleepers returns how many real `sleep` processes carry marker in their
// arguments, proving a race produced exactly one child.
//
// It deliberately does NOT use `pgrep -f marker`: that matches any process
// whose command line merely CONTAINS the marker, including this test binary,
// the editor, and any grep the developer is running — which made this test
// report a phantom second copy. Selecting on the executable name first
// (`pgrep -x sleep`) and only then checking arguments cannot self-match.
func countSleepers(t *testing.T, marker string) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-x", "sleep").Output()
	if err != nil {
		return 0 // pgrep exits 1 with no output when nothing matches
	}
	count := 0
	for _, pid := range strings.Fields(string(out)) {
		args, err := exec.Command("ps", "-o", "args=", "-p", pid).Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(args), marker) {
			count++
		}
	}
	return count
}
