package manager

import (
	"os/exec"
	"strconv"
	"strings"
)

// procEntry is one row of a process-table snapshot: the state code and start
// time exactly as `ps` reports them, so comparisons against the recorded
// StartTime string behave identically to the per-pid path.
type procEntry struct {
	state  string
	lstart string
}

// procTable is a point-in-time snapshot of every process on the box, taken with
// a single `ps` invocation.
//
// Without it, each watch tick asked `ps` twice per managed service — once for
// state (liveness/zombie) and once for lstart (PID-reuse defence) — so a box
// with ~40 running services fork+exec'd ~80 short-lived `ps` processes every
// second. That was the daemon's remaining CPU cost once the log-tree walk was
// rate-limited: a sample showed syscall.runtime_AfterFork, exec.(*Cmd).Start
// and os.Pipe with no other hot frames. One snapshot per tick collapses those
// ~80 forks into 1, and the extra rows for unmanaged processes cost only a
// string scan.
type procTable struct {
	byPID map[int]procEntry
}

// newProcTable snapshots the process table. It returns nil if `ps` fails, and
// every lookup on a nil table reports "unknown" so callers transparently fall
// back to their per-pid queries — a snapshot is an optimisation, never a
// source of truth about a process being gone.
func newProcTable() *procTable {
	out, err := exec.Command("ps", "-Ao", "pid=,state=,lstart=").Output()
	if err != nil {
		return nil
	}
	table := &procTable{byPID: make(map[int]procEntry, 512)}
	for _, line := range strings.Split(string(out), "\n") {
		pid, entry, ok := parseProcLine(line)
		if !ok {
			continue
		}
		table.byPID[pid] = entry
	}
	if len(table.byPID) == 0 {
		return nil
	}
	return table
}

// parseProcLine splits one `ps -Ao pid=,state=,lstart=` row into its pid, state
// and lstart. The lstart field contains spaces, so it is taken as the whole
// remainder after the state column rather than by field count.
func parseProcLine(line string) (int, procEntry, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0, procEntry{}, false
	}
	pidText, rest, found := strings.Cut(trimmed, " ")
	if !found {
		return 0, procEntry{}, false
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return 0, procEntry{}, false
	}
	rest = strings.TrimSpace(rest)
	state, lstart, found := strings.Cut(rest, " ")
	if !found {
		return 0, procEntry{}, false
	}
	return pid, procEntry{state: state, lstart: strings.TrimSpace(lstart)}, true
}

// lookup returns the snapshot row for pid and whether pid was present. Callers
// must only use it on a non-nil table: an absent pid in a valid snapshot is a
// genuine "process is gone", whereas a nil table means "no snapshot, ask ps".
func (t *procTable) lookup(pid int) (procEntry, bool) {
	entry, ok := t.byPID[pid]
	return entry, ok
}

// snapshotProcs returns the process table the current watch tick is reading, or
// nil outside a tick (CLI invocations, which query single pids directly).
func (m *Manager) snapshotProcs() *procTable {
	m.procTableMu.Lock()
	defer m.procTableMu.Unlock()
	return m.procTable
}

// setProcSnapshot installs (or clears, with nil) the per-tick process snapshot.
func (m *Manager) setProcSnapshot(table *procTable) {
	m.procTableMu.Lock()
	m.procTable = table
	m.procTableMu.Unlock()
}
