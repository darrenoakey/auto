package manager

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestProcTableSnapshotsRealProcesses pins that the snapshot is built from a
// real `ps` and can answer for a real live process, including its lstart in the
// same form the per-pid path records.
func TestProcTableSnapshotsRealProcesses(t *testing.T) {
	table := newProcTable()
	if table == nil {
		t.Fatal("newProcTable returned nil against a live system")
	}
	self := os.Getpid()
	entry, present := table.lookup(self)
	if !present {
		t.Fatalf("snapshot missing our own pid %d (%d rows)", self, len(table.byPID))
	}
	if entry.state == "" {
		t.Fatal("snapshot state must not be empty for a live process")
	}
	if entry.lstart != processStartTime(self) {
		t.Fatalf("snapshot lstart %q != ps lstart %q", entry.lstart, processStartTime(self))
	}
}

// TestSnapshotIsOurProcessMatchesPerPidPath pins that the snapshot path and the
// two-`ps` path agree, since the snapshot must change only how the facts are
// obtained — never the verdict.
func TestSnapshotIsOurProcessMatchesPerPidPath(t *testing.T) {
	table := newProcTable()
	if table == nil {
		t.Fatal("newProcTable returned nil")
	}
	self := os.Getpid()
	start := processStartTime(self)

	// A live process with the right start time is ours on both paths.
	if !isOurProcessVia(nil, self, &start) {
		t.Fatal("per-pid path: our own live process must be ours")
	}
	if !isOurProcessVia(table, self, &start) {
		t.Fatal("snapshot path: our own live process must be ours")
	}

	// A wrong start time (PID reuse) is rejected on both paths.
	wrong := "Mon 1 Jan 00:00:00 2001"
	if isOurProcessVia(nil, self, &wrong) {
		t.Fatal("per-pid path: mismatched start time must be rejected")
	}
	if isOurProcessVia(table, self, &wrong) {
		t.Fatal("snapshot path: mismatched start time must be rejected")
	}

	// A dead pid is not ours on either path.
	dead := reapedPID(t)
	if isOurProcessVia(nil, dead, &start) {
		t.Fatal("per-pid path: reaped pid must not be ours")
	}
	if isOurProcessVia(table, dead, &start) {
		t.Fatal("snapshot path: reaped pid must not be ours")
	}

	// A nil recorded start time is stale on both paths.
	if isOurProcessVia(nil, self, nil) || isOurProcessVia(table, self, nil) {
		t.Fatal("nil recorded start time must be treated as stale on both paths")
	}
}

// TestSnapshotRejectsZombies pins that the snapshot applies the zombie check
// the per-pid path gets from `ps -o state=`, so a defunct child is never
// reported as a live managed process.
func TestSnapshotRejectsZombies(t *testing.T) {
	start := "irrelevant"
	for _, state := range []string{"Z", "Z+", ""} {
		table := &procTable{byPID: map[int]procEntry{4242: {state: state, lstart: start}}}
		if isOurProcessVia(table, 4242, &start) {
			t.Fatalf("state %q must not count as a live process", state)
		}
	}
}

// TestParseProcLineHandlesSpacedLstart pins that the lstart column — which
// itself contains spaces — is taken as the whole remainder rather than by field
// count, in both locale layouts ps emits.
func TestParseProcLineHandlesSpacedLstart(t *testing.T) {
	cases := map[string]struct {
		pid           int
		state, lstart string
	}{
		"  1234 Ss   Mon 27 Jul 12:43:33 2026": {1234, "Ss", "Mon 27 Jul 12:43:33 2026"},
		"99 S Mon Jul 27 12:43:33 2026":        {99, "S", "Mon Jul 27 12:43:33 2026"},
	}
	for line, want := range cases {
		pid, entry, ok := parseProcLine(line)
		if !ok {
			t.Fatalf("parseProcLine(%q) failed", line)
		}
		if pid != want.pid || entry.state != want.state || entry.lstart != want.lstart {
			t.Fatalf("parseProcLine(%q) = %d/%q/%q, want %d/%q/%q",
				line, pid, entry.state, entry.lstart, want.pid, want.state, want.lstart)
		}
		if _, ok := parseLstartTime(entry.lstart); !ok {
			t.Fatalf("lstart %q must parse as a start time", entry.lstart)
		}
	}
	for _, bad := range []string{"", "   ", "notapid S Mon Jul 27 12:43:33 2026", "1234"} {
		if _, _, ok := parseProcLine(bad); ok {
			t.Fatalf("parseProcLine(%q) must not parse", bad)
		}
	}
}

// TestSpawnInvalidatesTickSnapshot pins the double-spawn guard: a snapshot
// taken before a spawn would report the freshly started pid as dead, so
// recordStarted must drop it. Without this a single tick could start the same
// service twice.
func TestSpawnInvalidatesTickSnapshot(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "snap-svc", "sleep 300", nil)
	m.setProcSnapshot(newProcTable())
	if m.snapshotProcs() == nil {
		t.Fatal("expected a snapshot to be installed")
	}

	if _, err := m.StartProcess("snap-svc"); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("snap-svc", true) })

	if m.snapshotProcs() != nil {
		t.Fatal("recordStarted must clear the pre-spawn snapshot")
	}
	if _, alive := m.processStatus("snap-svc"); !alive {
		t.Fatal("freshly started process must read as alive, not as dead from a stale snapshot")
	}
}

// TestSnapshotNegativeIsReverifiedAgainstLiveProcess pins the double-spawn
// regression found by the canary probe: `auto start` from the CLI is a separate
// process, so a pid it spawns and records is legitimately absent from a
// snapshot the daemon took microseconds earlier. Treating that absence as
// authoritative made the daemon restart a healthy just-started service and
// orphan the first copy. A snapshot may short-circuit a positive only; every
// negative must be re-verified against a live ps.
func TestSnapshotNegativeIsReverifiedAgainstLiveProcess(t *testing.T) {
	self := os.Getpid()
	start := processStartTime(self)

	// A snapshot that does not know about a genuinely live pid — exactly what a
	// pre-spawn snapshot looks like to the daemon.
	stale := &procTable{byPID: map[int]procEntry{}}
	if snapshotIsOurProcess(stale, self, &start) {
		t.Fatal("precondition: the stale snapshot must not know this pid")
	}
	if !isOurProcessVia(stale, self, &start) {
		t.Fatal("a live process missing from a stale snapshot must be re-verified as ours, not reported dead")
	}

	// The re-verification must not resurrect a genuinely dead pid.
	dead := reapedPID(t)
	if isOurProcessVia(stale, dead, &start) {
		t.Fatal("re-verification must still report a reaped pid as not ours")
	}
}

// TestWatchTickClearsSnapshotAfterwards pins that the snapshot never outlives
// its tick, so CLI-style calls between ticks always see current reality.
func TestWatchTickClearsSnapshotAfterwards(t *testing.T) {
	m := newTestManager(t)
	m.WatchTick()
	if m.snapshotProcs() != nil {
		t.Fatal("snapshot must be cleared when the tick returns")
	}
}

// reapedPID returns a pid that has exited and been waited on, so it is neither
// alive nor a zombie.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	pid := cmd.Process.Pid
	if out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "state=").Output(); err == nil {
		if strings.TrimSpace(string(out)) != "" {
			t.Skipf("pid %d still visible to ps; cannot test a reaped pid deterministically", pid)
		}
	}
	return pid
}
