package manager

import (
	"fmt"
	"os"
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
//
// The actual fork/exec is only ever performed by the watch daemon's own
// process (see spawnDirect / delegateSpawnToDaemon below): a child spawned
// directly by an arbitrary CLI caller (an agent session, an editor, a
// terminal) inherits THAT caller's macOS "responsible process" identity and
// XNU resource coalition instead of Auto.app's signed one, which is invisible
// to `ps`/`footprint` but corrupts coalition-aggregated system UI such as
// Force Quit Applications / memory-pressure dialogs (observed: two tiny,
// healthy ~100MB managed services both reporting several GB, identically,
// because they and the agent session that had run `auto restart` on them all
// shared one coalition). If the daemon is running and this call is not it,
// StartProcess delegates; otherwise (daemon down, or this call IS the
// daemon) it spawns directly as before.
func (m *Manager) StartProcess(name string) (int, error) {
	def, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if def.Isolate {
		return m.isolateStart(name, def.Command, def.Workdir)
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
	if daemonPid := m.liveDaemon(); daemonPid != 0 && daemonPid != os.Getpid() {
		return m.delegateSpawnToDaemon(name)
	}
	return m.spawnDirect(name, def)
}

// spawnDirect performs the actual fork/exec in the calling process. Callers
// must only use this when the calling process IS the watch daemon (its own
// spawns are correctly owned by Auto.app) or when no live daemon heartbeat is
// recorded to delegate to (e.g. before the daemon has ticked for the first
// time, or in an isolated test Manager whose state file no watch loop ticks).
func (m *Manager) spawnDirect(name string, def *Process) (int, error) {
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

// DaemonHeartbeatTTL bounds how stale a recorded daemon heartbeat may be
// before it is treated as gone. The daemon writes a fresh one at the top of
// every ~1s WatchTick (see recordHeartbeat in watch.go), so 3x that
// comfortably tolerates one slow tick without flapping between delegated and
// direct spawning.
const DaemonHeartbeatTTL = 3 * time.Second

// liveDaemon returns the pid of the process currently watching THIS state
// file, or 0 if none has ticked recently. Scoped to this Manager's own state
// file rather than a system-wide `launchctl` query, so an isolated test
// Manager — whose temp state file no watch loop ever ticks — never mistakes
// an unrelated production daemon elsewhere on the machine for its own.
func (m *Manager) liveDaemon() int {
	data := m.loadStateFile()
	if data.DaemonHeartbeat == nil || data.DaemonPid == nil {
		return 0
	}
	if nowUnix()-*data.DaemonHeartbeat > DaemonHeartbeatTTL.Seconds() {
		return 0
	}
	return *data.DaemonPid
}

// SpawnRequestTTL bounds how long a delegated spawn request waits for the
// watch daemon to fulfill it before the requester gives up, and how long the
// daemon itself will still honor a request it did not reach immediately. Sized
// above the daemon's 1s tick period plus the worst-case spawn budget
// (SpawnRetryAttempts retries at up to SpawnRetryBaseDelay*attempt apart, plus
// SpawnVerifyDelay each) so a slow-but-legitimate spawn is never mistaken for
// an abandoned request.
const SpawnRequestTTL = 20 * time.Second

// delegateSpawnToDaemon asks the already-running watch daemon to perform the
// fork/exec for name, then blocks until the daemon reports the process alive
// or SpawnRequestTTL elapses. See StartProcess for why this indirection
// exists: only the daemon's own process may fork/exec a managed service.
func (m *Manager) delegateSpawnToDaemon(name string) (int, error) {
	m.mutateProcess(name, func(p *Process) {
		now := nowUnix()
		p.SpawnRequestedAt = &now
	})
	deadline := time.Now().Add(SpawnRequestTTL)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if pid, alive := m.processStatus(name); alive {
			return pid, nil
		}
	}
	return 0, fmt.Errorf(
		"watch daemon did not spawn %s within %s; check `auto log %s` and that `auto watch` is running",
		name, SpawnRequestTTL, name)
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

// clearSpawnRequest releases a delegated spawn request, whether fulfilled,
// found already satisfied by a race, or discovered stale/abandoned.
func (m *Manager) clearSpawnRequest(name string) {
	m.mutateProcess(name, func(p *Process) { p.SpawnRequestedAt = nil })
}

// isSpawnRequestLive reports whether a delegated spawn request is present and
// still within SpawnRequestTTL. Mirrors startClaimIsLive: an expired request
// means the requesting CLI invocation gave up (or died) before the daemon
// reached it, so the daemon must not spawn a delayed, unwanted copy long
// after the caller stopped waiting.
func isSpawnRequestLive(p *Process) bool {
	if p == nil || p.SpawnRequestedAt == nil {
		return false
	}
	return nowUnix()-*p.SpawnRequestedAt < SpawnRequestTTL.Seconds()
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
	def, ok := m.definition(name)
	if !ok {
		return fmt.Errorf("process %s not found in config", name)
	}
	if def.Isolate {
		if err := m.isolateStop(name); err != nil {
			return err
		}
		if markExplicit {
			m.markExplicitlyStopped(name)
		}
		return nil
	}
	pid, alive := m.processStatus(name)
	if !alive {
		return m.stopDeadProcess(name, markExplicit)
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	if markExplicit {
		m.markExplicitlyStopped(name)
	}
	if err := m.terminateProcessGroup(name, pid, pgid); err != nil {
		return err
	}
	m.clearRuntimeIdentity(name)
	m.freePortAfterStop(name)
	return nil
}

// stopDeadProcess makes an explicit stop durable when process death won the
// race with the user's stop request.
func (m *Manager) stopDeadProcess(name string, markExplicit bool) error {
	if !markExplicit {
		return fmt.Errorf("process %s is not running", name)
	}
	m.persistStoppedRuntime(name)
	return nil
}

// terminateProcessGroup signals a live process group and verifies its death.
func (m *Manager) terminateProcessGroup(name string, pid, pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	return m.escalateKill(name, pid, pgid)
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

// persistStoppedRuntime records an explicit stop and removes stale ownership
// fields when no live owned process remains.
func (m *Manager) persistStoppedRuntime(name string) {
	m.mutateProcess(name, func(p *Process) {
		p.ExplicitlyStopped = true
		resetRuntimeIdentity(p)
	})
}

// clearRuntimeIdentity removes ownership fields after process death.
func (m *Manager) clearRuntimeIdentity(name string) {
	m.mutateProcess(name, resetRuntimeIdentity)
}

// resetRuntimeIdentity removes runtime ownership from one process entry.
func resetRuntimeIdentity(process *Process) {
	process.Pid = nil
	process.StartTime = nil
	process.StartingSince = nil
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
	if def.Isolate {
		pid, err := m.isolateRestart(name, def.Command, def.Workdir)
		if err != nil {
			return 0, err
		}
		m.clearExplicitStop(name)
		return pid, nil
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
