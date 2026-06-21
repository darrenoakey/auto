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
	expectedDt, eok := parseLstartTime(*expectedStartTime)
	actualDt, aok := parseLstartTime(actual)
	if !eok || !aok {
		return actual == *expectedStartTime
	}
	return expectedDt.Equal(actualDt)
}
