package manager

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// lstartLayouts are the locale-dependent formats historically emitted by
// `ps -o lstart=`. US locale yields "Mon Jan 26 10:35:12 2026"; en_AU yields
// "Mon 26 Jan 10:57:01 2026". Both space-padded and unpadded days are
// accepted. They are still parsed because state files written by earlier
// releases record start times in exactly these forms.
var lstartLayouts = []string{
	"Mon Jan _2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
	"Mon _2 Jan 15:04:05 2006",
	"Mon 2 Jan 15:04:05 2006",
}

// psLstartLayout is the format new start-time identity strings are rendered
// in: the US ps form, which parseLstartTime accepts. Rendering the kernel's
// start timeval into the same family of strings keeps stored identity
// comparable across releases without a format migration.
const psLstartLayout = "Mon Jan _2 15:04:05 2006"

// procStateZombie is Darwin's SZOMB from sys/proc.h: the process exited but
// has not been reaped. A zombie owns no live execution, so supervision must
// treat it as dead. P_stat 0 is likewise never a live process.
const procStateZombie = 5

// kernelProc answers one targeted kernel query for a single pid via the
// kern.proc.pid sysctl. It never enumerates the process table and never
// forks a helper process: the syscall returns exactly one kinfo_proc or an
// error for a pid that no longer exists.
func kernelProc(pid int) (*unix.KinfoProc, error) {
	if pid <= 0 {
		return nil, syscall.ESRCH
	}
	return unix.SysctlKinfoProc("kern.proc.pid", pid)
}

// isProcessAlive reports whether a process with the given pid is running and
// is not a zombie. Existence is confirmed with signal 0 (EPERM means another
// user's live process), and the zombie check reads the kernel's scheduler
// state from the same per-pid sysctl that supplies the start time.
func isProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	info, err := kernelProc(pid)
	if err != nil {
		return false
	}
	state := info.Proc.P_stat
	return state != 0 && state != procStateZombie
}

// processStartTime returns the pid's start time rendered as a ps-compatible
// lstart string, or "" if the process is gone. The instant comes from the
// kernel's kern.proc.pid sysctl.
func processStartTime(pid int) string {
	info, err := kernelProc(pid)
	if err != nil {
		return ""
	}
	return renderLstartTime(info.Proc.P_starttime)
}

// renderLstartTime renders a kernel start timeval as the ps-compatible local
// wall-clock string used for stored process identity, so strings written by
// this code and strings written by earlier ps-based releases parse and
// compare identically.
func renderLstartTime(tv unix.Timeval) string {
	return time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond)).Format(psLstartLayout)
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

// isOurProcess reports whether pid is alive AND matches the recorded start
// time. This defeats PID reuse after a reboot: a recycled pid carries a
// different kernel start time than the one retained at spawn. A missing
// recorded start time is treated as stale (returns false) so the process is
// restarted with proper tracking, exactly as the Python implementation did.
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
	return startTimesMatch(actual, *expectedStartTime)
}

// startTimesMatch compares two ps lstart strings, parsing both when possible
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
