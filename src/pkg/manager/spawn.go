package manager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// transientSpawnErrnos are host fork/exec failures that are not the service's
// fault: the host momentarily cannot fork/exec under load. They are retried
// rather than counted against the restart backoff.
var transientSpawnErrnos = []error{syscall.EDEADLK, syscall.EAGAIN, syscall.ENOMEM}

// spawnWithRetry launches `exec <command>` via /bin/sh in a new session and
// returns the running process and its log path. It retries both spawn failure
// shapes seen under heavy host load: the parent fork/exec raising a transient
// errno, and the child shell's execve failing asynchronously (detected by a
// transient marker in the log after the child dies within the grace period).
func (m *Manager) spawnWithRetry(name, command, workdir string) (int, string, error) {
	wrapped := "exec " + command
	var lastErr error
	for attempt := 0; attempt < SpawnRetryAttempts; attempt++ {
		pid, logPath, offset, err := m.spawnOnce(name, wrapped, workdir)
		if err != nil {
			if !isTransientSpawnError(err) {
				return 0, "", err
			}
			lastErr = err
			sleepSpawnBackoff(name, attempt)
			continue
		}
		if childStillRunning(pid) {
			reapWhenDone(pid)
			return pid, logPath, nil
		}
		if !logHasTransientExecError(logPath, offset) {
			return pid, logPath, nil // exited fast for a non-transient reason
		}
		lastErr = fmt.Errorf("%s: shell execve failed transiently", name)
		sleepSpawnBackoff(name, attempt)
	}
	if lastErr != nil {
		return 0, "", lastErr
	}
	return 0, "", fmt.Errorf("cannot start %s: no spawn attempts were made", name)
}

// spawnOnce performs a single fork/exec and waits the grace period implicitly via
// the caller's liveness check. It returns the child pid, its log path, and the
// byte offset at which this spawn's output begins (the file is appended to, so
// the caller only inspects content written from offset onward).
func (m *Manager) spawnOnce(name, wrapped, workdir string) (int, string, int64, error) {
	logPath := m.dailyLogPath(name)
	offset := fileSize(logPath)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, "", 0, err
	}
	defer func() { _ = logFile.Close() }()
	cmd := exec.Command("/bin/sh", "-c", wrapped)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if workdir != "" {
		cmd.Dir = workdir
	}
	if err := cmd.Start(); err != nil {
		return 0, "", 0, err
	}
	pid := cmd.Process.Pid
	time.Sleep(SpawnVerifyDelay)
	return pid, logPath, offset, nil
}

// fileSize returns the current size of the file, or 0 if it does not exist.
func fileSize(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// childStillRunning reports whether the child is alive, reaping it if it has
// already exited so it does not linger as a zombie.
func childStillRunning(pid int) bool {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return err == nil && wpid == 0
}

// reapWhenDone blocks in a background goroutine until the child exits and reaps
// it, preventing zombie accumulation in the long-lived watch daemon.
func reapWhenDone(pid int) {
	go func() {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	}()
}

// sleepSpawnBackoff waits between spawn retries, jittered per-name so concurrent
// spawns do not retry in lockstep.
func sleepSpawnBackoff(name string, attempt int) {
	if attempt >= SpawnRetryAttempts-1 {
		return
	}
	delay := SpawnRetryBaseDelay*time.Duration(attempt+1) + time.Duration(restartJitter(name))*10*time.Millisecond
	time.Sleep(delay)
}

// isTransientSpawnError reports whether a spawn error is a transient host
// fork/exec failure worth retrying.
func isTransientSpawnError(err error) bool {
	for _, target := range transientSpawnErrnos {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// logHasTransientExecError reports whether the portion of the log written by the
// current spawn (from offset onward) contains a marker of an asynchronous
// transient execve failure. Scoping to the spawn's own bytes avoids a stale
// marker from an earlier spawn the same day being read as this one's failure.
func logHasTransientExecError(logPath string, offset int64) bool {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return false
	}
	if offset < 0 || offset > int64(len(raw)) {
		offset = 0
	}
	text := string(raw[offset:])
	for _, marker := range transientExecMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
