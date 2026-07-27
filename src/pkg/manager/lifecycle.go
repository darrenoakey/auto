package manager

import (
	"fmt"
	"syscall"
	"time"
)

// afterPortCheckHook, when non-nil, runs after StartProcess confirms a
// configured port is free and before it spawns the new process. It exists
// only so tests can deterministically occupy the TOCTOU window between that
// check and the child's own bind() call; production code never sets it.
var afterPortCheckHook func(port int)

// StartProcess launches a configured process, force-freeing its port first, and
// records its pid and start time for identity verification. If the new
// process still loses the race for its port (something re-grabs it between
// this check and its own bind), spawnWithRetry forces the port free again and
// retries within this same call so the caller sees a single converged start.
func (m *Manager) StartProcess(name string) (int, error) {
	def, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if pid, alive := m.processStatus(name); alive {
		return 0, fmt.Errorf("process %s is already running with pid %d", name, pid)
	}
	if def.Port != nil && !isPortFree(*def.Port) && !forceFreePort(*def.Port) {
		return 0, fmt.Errorf(
			"cannot start %s: port %d still in use after killing all holders. Check with: lsof -i :%d",
			name, *def.Port, *def.Port)
	}
	if afterPortCheckHook != nil && def.Port != nil {
		afterPortCheckHook(*def.Port)
	}
	// Claim the start before spawning: for the whole spawn window the entry
	// still has Pid=nil, and a concurrent WatchTick reading that would start a
	// competing copy (observed: `auto add` and the watch loop each spawning one
	// canary, orphaning the CLI's). Both read state only through the lock, so
	// once this write lands the loop can never see an unclaimed dead entry.
	if !m.claimStart(name) {
		return 0, fmt.Errorf("process %s is already starting", name)
	}
	pid, logPath, err := m.spawnWithRetry(name, def.Command, def.Workdir, def.Port)
	if err != nil {
		m.clearStartClaim(name)
		return 0, err
	}
	m.recordStarted(name, pid, logPath)
	return pid, nil
}

// StartInFlightTTL bounds how long a start claim suppresses supervision. A
// claim is normally cleared within a second, but the claiming process can die
// mid-spawn (a CLI killed, or the daemon torn down by `auto install`), and a
// stale claim must never wedge a service out of supervision permanently. The
// TTL sits comfortably above the worst-case spawn budget
// (SpawnRetryAttempts * SpawnRetryBaseDelay * attempts + SpawnVerifyDelay).
const StartInFlightTTL = 60 * time.Second

// claimStart records that a start is in flight, returning false if another
// start already holds a live claim.
func (m *Manager) claimStart(name string) bool {
	claimed := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || startClaimIsLive(p) {
			return false
		}
		now := nowUnix()
		p.StartingSince = &now
		claimed = true
		return true
	})
	return claimed
}

// clearStartClaim releases an in-flight start claim.
func (m *Manager) clearStartClaim(name string) {
	m.mutateProcess(name, func(p *Process) { p.StartingSince = nil })
}

// startClaimIsLive reports whether a start claim is present and still within
// StartInFlightTTL. An expired claim is ignored so a claimant that died
// mid-spawn cannot suppress supervision forever.
func startClaimIsLive(p *Process) bool {
	if p == nil || p.StartingSince == nil {
		return false
	}
	return nowUnix()-*p.StartingSince < StartInFlightTTL.Seconds()
}

// recordStarted writes the runtime fields of a freshly started process,
// preserving its restart bookkeeping. If the definition vanished mid-start
// (concurrently removed) it records nothing rather than recreating a stub.
//
// It is the single choke point for every new pid, so it also drops any
// per-tick process-table snapshot: that snapshot predates this spawn and would
// report the new pid as dead, which could restart a service twice in one tick.
// Later lookups in the tick fall back to querying `ps` directly.
func (m *Manager) recordStarted(name string, pid int, logPath string) {
	st := processStartTime(pid)
	m.setProcSnapshot(nil)
	m.mutateProcess(name, func(p *Process) {
		p.Pid = &pid
		p.StartTime = &st
		p.ExplicitlyStopped = false
		p.LogPath = logPath
		p.StartingSince = nil // the pid is recorded; supervision may resume
	})
}

// definition returns the configured definition for a process.
func (m *Manager) definition(name string) (*Process, bool) {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok || p.Command == "" {
		return nil, false
	}
	return p, true
}

// waitForProcessDeath polls until a pid is gone or the timeout elapses.
func waitForProcessDeath(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !isProcessAlive(pid)
}

// StopProcess terminates a running process group (SIGTERM then SIGKILL) and, by
// default, marks it explicitly stopped so the watch loop will not restart it.
// When markExplicit is set, the flag is written BEFORE the kill signal is even
// sent, not after the process is confirmed dead: a concurrent WatchTick in the
// daemon's watch loop reads state only through the lock, so once this write
// lands it can never observe the process as "dead and not explicitly
// stopped" and race a competing respawn into the gap while this call is still
// tearing the old instance down.
func (m *Manager) StopProcess(name string, markExplicit bool) error {
	pid, alive := m.processStatus(name)
	if !alive {
		return fmt.Errorf("process %s is not running", name)
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	if markExplicit {
		m.markExplicitlyStopped(name)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	if err := m.escalateKill(name, pid, pgid); err != nil {
		return err
	}
	m.freePortAfterStop(name)
	return nil
}

// escalateKill waits for SIGTERM to take effect, escalating to SIGKILL and
// erroring only if the process survives both.
func (m *Manager) escalateKill(name string, pid, pgid int) error {
	if waitForProcessDeath(pid, SigtermTimeout) {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("failed to SIGKILL process %s with pid %d: %w", name, pid, err)
	}
	if !waitForProcessDeath(pid, SigkillTimeout) {
		return fmt.Errorf("process %s with pid %d survived both SIGTERM and SIGKILL", name, pid)
	}
	return nil
}

// freePortAfterStop best-effort frees a configured port after stopping, catching
// orphaned children that escaped the process group.
func (m *Manager) freePortAfterStop(name string) {
	if def, ok := m.definition(name); ok && def.Port != nil && !isPortFree(*def.Port) {
		forceFreePort(*def.Port)
	}
}

// markExplicitlyStopped sets the explicitly-stopped flag.
func (m *Manager) markExplicitlyStopped(name string) {
	m.mutateProcess(name, func(p *Process) { p.ExplicitlyStopped = true })
}

// clearExplicitStop clears the explicitly-stopped flag so the watch loop may
// manage the process again.
func (m *Manager) clearExplicitStop(name string) {
	m.mutateProcess(name, func(p *Process) { p.ExplicitlyStopped = false })
}

// obliterateProcess kills a process with escalating force and verifies death.
func (m *Manager) obliterateProcess(name string, pid int, markExplicit bool) error {
	if err := m.StopProcess(name, markExplicit); err != nil {
		return err
	}
	if !isProcessAlive(pid) {
		return nil
	}
	killProcessGroup(pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if !waitForProcessDeath(pid, SigkillTimeout) {
		return fmt.Errorf("process %s (pid %d) cannot be killed", name, pid)
	}
	return nil
}

// ensurePortFree verifies a port is free, force-killing holders, and errors if it
// cannot be freed.
func ensurePortFree(port *int, name string) error {
	if port == nil || isPortFree(*port) {
		return nil
	}
	if !forceFreePort(*port) {
		return fmt.Errorf(
			"cannot start %s: port %d still in use after killing all holders. Check: lsof -i :%d",
			name, *port, *port)
	}
	return nil
}

// RestartProcess stops, obliterates, frees the port, and starts a process. It
// marks the process explicitly stopped during the operation to block the watch
// loop, clearing the flag if the start fails so the loop can recover.
func (m *Manager) RestartProcess(name string) (int, error) {
	def, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if pid, alive := m.processStatus(name); alive {
		if err := m.obliterateProcess(name, pid, true); err != nil {
			m.clearExplicitStop(name)
			return 0, err
		}
	}
	if err := ensurePortFree(def.Port, name); err != nil {
		m.clearExplicitStop(name)
		return 0, err
	}
	pid, err := m.StartProcess(name)
	if err != nil {
		m.clearExplicitStop(name)
		return 0, err
	}
	m.resetRestartAttempt(name)
	return pid, nil
}
