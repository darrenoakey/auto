package manager

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lstartLayouts are the locale-dependent formats emitted by `ps -o lstart=`.
// US locale yields "Mon Jan 26 10:35:12 2026"; en_AU yields
// "Mon 26 Jan 10:57:01 2026". Both space-padded and unpadded days are accepted.
var lstartLayouts = []string{
	"Mon Jan _2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
	"Mon _2 Jan 15:04:05 2006",
	"Mon 2 Jan 15:04:05 2006",
}

// isProcessAlive reports whether a process with the given pid is running and is
// not a zombie. macOS has no /proc, so liveness is confirmed with signal 0 and
// the zombie check is done via ps.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "state=").Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	if state == "" {
		return false
	}
	return state != "Z" && state != "Z+"
}

// processStartTime returns the start-time string for a pid (as ps reports it),
// or "" if the process is gone.
func processStartTime(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseLstartTime parses a ps lstart string, tolerating locale differences.
func parseLstartTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range lstartLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// isOurProcess reports whether pid is alive AND matches the recorded start time.
// This defeats PID reuse after a reboot. A missing recorded start time is
// treated as stale (returns false) so the process is restarted with proper
// tracking, exactly as the Python implementation did.
func isOurProcess(pid int, expectedStartTime *string) bool {
	return isOurProcessVia(nil, pid, expectedStartTime)
}

// isOurProcessVia is isOurProcess against an optional process-table snapshot.
// The watch loop supplies a snapshot so a tick costs one `ps` rather than two
// per managed service (see procTable).
//
// A snapshot can only ever SHORT-CIRCUIT A POSITIVE. A negative always falls
// through to the authoritative per-pid `ps`, because the snapshot may predate a
// spawn it cannot see: `auto start` from the CLI is a separate process that
// writes the new pid into the state file the daemon then reads, so that pid is
// legitimately absent from a snapshot taken microseconds earlier. Trusting that
// absence made the daemon declare a just-started service dead and start it
// again, orphaning the first copy (observed: canary-probe 83444 orphaned by a
// restart to 83447). Negatives are rare — a service that genuinely looks dead
// is about to be forked anyway — so this keeps the steady-state fork count at
// zero while making a spurious double-spawn impossible.
//
// The residual imprecision is unchanged from the pre-snapshot code: a process
// that dies mid-tick is noticed on the next tick rather than this one.
func isOurProcessVia(table *procTable, pid int, expectedStartTime *string) bool {
	if table != nil && snapshotIsOurProcess(table, pid, expectedStartTime) {
		return true
	}
	if !isProcessAlive(pid) {
		return false
	}
	if expectedStartTime == nil {
		return false
	}
	actual := processStartTime(pid)
	if actual == "" {
		return false
	}
	return startTimesMatch(actual, *expectedStartTime)
}

// snapshotIsOurProcess answers isOurProcess from a process-table snapshot,
// applying the same alive / not-zombie / start-time-matches rules as the
// per-pid `ps` path. A pid absent from a valid snapshot is genuinely gone.
func snapshotIsOurProcess(table *procTable, pid int, expectedStartTime *string) bool {
	if pid <= 0 || expectedStartTime == nil {
		return false
	}
	entry, present := table.lookup(pid)
	if !present {
		return false
	}
	if entry.state == "" || entry.state == "Z" || entry.state == "Z+" {
		return false
	}
	if entry.lstart == "" {
		return false
	}
	return startTimesMatch(entry.lstart, *expectedStartTime)
}

// startTimesMatch compares two `ps` lstart strings, parsing both when possible
// so locale differences in layout do not cause a false mismatch, and falling
// back to an exact string compare when either side is unparseable.
func startTimesMatch(actual, expected string) bool {
	actualDt, aok := parseLstartTime(actual)
	expectedDt, eok := parseLstartTime(expected)
	if !eok || !aok {
		return actual == expected
	}
	return expectedDt.Equal(actualDt)
}
