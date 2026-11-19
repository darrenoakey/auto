package manager

import (
	"fmt"
	"syscall"
	"time"
)

// TermOnlyGrace bounds how long a term-only restart waits for the old process
// to exit on its own after one SIGTERM to its process group. After the grace
// elapses the old process is left untouched (never SIGKILLed, never re-sent)
// and the restart fails without spawning anything.
const TermOnlyGrace = 20 * time.Second

// FeatureTermOnly is the watch-daemon capability bit for term-only restarts.
// A daemon built from this code sets it in every heartbeat. The CLI refuses
// to begin a term-only restart unless the currently-live daemon carries it:
// an older watcher does not know the TermStop hold fields and its periodic or
// crash-restart paths would SIGKILL the process mid-grace. The check is
// mechanical — it does not rely on the operator having deployed in the right
// order — and it fails closed: stale or missing heartbeat, or a heartbeat
// without the bit, means no signal is ever sent.
const FeatureTermOnly = 1

// TermStopRequestTTL bounds the in-flight window of a term-only request. It
// covers TermOnlyGrace plus the worst-case delegated spawn budget for the
// replacement (SpawnRequestTTL). A request whose requester vanished (CLI
// killed mid-restart) drains at this TTL, so one lost invocation can never
// wedge a service out of supervision forever — the same reasoning as
// StartInFlightTTL. A request that TIMED OUT (old process refused TERM) is
// different: its marker has no TTL, because resuming supervision would
// promptly SIGKILL the process that just declined to terminate.
const TermStopRequestTTL = TermOnlyGrace + SpawnRequestTTL

// termStopHoldActive reports whether an automatic supervision action on name
// must be withheld right now: a term-only restart is either in flight or has
// failed at its grace timeout, leaving a live process that explicitly must
// not be force-killed. WatchTick checks this before periodic restarts, orphan
// adoption, and crash restarts. Delegated spawn fulfillment deliberately runs
// BEFORE this gate — the term-only restart's own replacement start arrives as
// a delegated spawn request.
func termStopHoldActive(p *Process) bool {
	if p == nil {
		return false
	}
	if p.TermStopTimedOut {
		return true
	}
	if p.TermStopRequestedAt == nil {
		return false
	}
	return nowUnix()-*p.TermStopRequestedAt < TermStopRequestTTL.Seconds()
}

// daemonSupportsTermOnly verifies that the watch daemon currently owning this
// state file's heartbeat both is live and advertises FeatureTermOnly. This is
// the fail-closed pre-signal gate against an older watcher: an old daemon
// never writes daemon_features (and drops a stale one on its first save),
// so it can never authorize a SIGTERM from this path.
func (m *Manager) daemonSupportsTermOnly() error {
	data := m.loadStateFile()
	if data.DaemonHeartbeat == nil || data.DaemonPid == nil ||
		nowUnix()-*data.DaemonHeartbeat > DaemonHeartbeatTTL.Seconds() {
		return fmt.Errorf("term-only restart requires a live watch daemon with term-only support; none has a current heartbeat")
	}
	if data.DaemonFeatures == nil || *data.DaemonFeatures&FeatureTermOnly == 0 {
		return fmt.Errorf(
			"watch daemon pid %d does not advertise term-only support; the updated watcher must be running before any term-only restart",
			*data.DaemonPid)
	}
	return nil
}

// beginTermStop durably records a term-only restart intent for name under the
// state lock, verifying that the recorded runtime identity still names exactly
// the pid this request targeted. The hold is written BEFORE any signal is
// sent, mirroring the StopProcess ordering for ExplicitlyStopped, so a
// concurrent WatchTick can never observe an unheld process in the gap.
func (m *Manager) beginTermStop(name string, pid int) error {
	started := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.Command == "" {
			return false
		}
		if termStopHoldActive(p) {
			return false
		}
		if p.Pid == nil || *p.Pid != pid || p.StartTime == nil || !isOurProcess(pid, p.StartTime) {
			return false
		}
		// Snapshot the ownership epoch with the request: the signal transaction
		// re-verifies it below, so no racing ownership write can be signalled.
		if p.TermStopGeneration == 0 {
			p.TermStopGeneration = 1
		}
		p.TermStopRequestGeneration = p.TermStopGeneration
		now := nowUnix()
		p.TermStopRequestedAt = &now
		p.TermStopOriginalPid = p.Pid
		p.TermStopOriginalStartTime = p.StartTime
		started = true
		return true
	})
	if !started {
		return fmt.Errorf(
			"process %s cannot start a term-only restart: another term-only request is in flight or timed out, or its recorded identity changed",
			name)
	}
	return nil
}

// signalTermStopVerified delivers the restart's single SIGTERM to the target's
// process group from INSIDE the exclusive state transaction, after re-making
// the ownership check beginTermStop made. Every path that could kill or
// replace the old instance (operator stop, serial restart, watch-loop
// restart) must first write state under this same lock — StopProcess writes
// ExplicitlyStopped before signalling, restarts record the new pid — so if
// any of them won the race this critical section observes the changed
// ownership and sends nothing, rather than TERMed a recycled process group.
func (m *Manager) signalTermStopVerified(name string, pid, pgid int) error {
	sent := false
	var signalError error
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.TermStopRequestedAt == nil || p.TermStopOriginalPid == nil ||
			*p.TermStopOriginalPid != pid || p.Pid == nil || *p.Pid != pid ||
			!isOurProcess(pid, p.StartTime) || p.ExplicitlyStopped ||
			p.TermStopGeneration != p.TermStopRequestGeneration {
			return false
		}
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			signalError = err
			return false
		}
		sent = true
		return false // signal performed; state deliberately unchanged
	})
	if signalError != nil {
		return fmt.Errorf("failed to signal process %s with pid %d: %w", name, pid, signalError)
	}
	if !sent {
		return fmt.Errorf("ownership of process %s changed before its term-only signal; no signal was sent", name)
	}
	return nil
}

// abandonTermStop clears an in-flight term-only request whose replacement
// start failed before any graceful exit, releasing supervision without
// touching the old process.
func (m *Manager) abandonTermStop(name string) {
	m.mutateProcess(name, func(p *Process) {
		p.TermStopRequestedAt = nil
		p.TermStopOriginalPid = nil
		p.TermStopOriginalStartTime = nil
	})
}

// RestartProcessTermOnly restarts a LIVE process by asking it to terminate:
// exactly one SIGTERM to its process group, then up to TermOnlyGrace waiting
// for it to exit on its own. On self-exit the port is awaited (never forced)
// and the replacement starts through the ordinary StartProcess path, which
// delegates the fork/exec to the watch daemon when one is live. On grace
// timeout nothing further happens to the old process: no SIGKILL, no second
// signal, no spawn; a durable TermStopTimedOut marker inhibits automatic
// supervision — including after the old process later exits on its own —
// until the operator resolves the service (a later successful start or a
// completed stop clears it).
func (m *Manager) RestartProcessTermOnly(name string) (int, error) {
	return m.restartProcessTermOnly(name, TermOnlyGrace)
}

// restartProcessTermOnly is RestartProcessTermOnly with an explicit grace
// period, so tests exercise the real signal/death/marker machinery against
// real processes in seconds instead of 20.
func (m *Manager) restartProcessTermOnly(name string, grace time.Duration) (int, error) {
	if err := m.checkStartsAllowed(); err != nil {
		return 0, err
	}
	def, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if def.Isolate {
		return 0, fmt.Errorf("term-only restart is not supported for launchd-isolated process %s", name)
	}
	// Fail closed BEFORE any state change or signal: an older watcher must
	// never be surprised by a TERM it cannot supervise around.
	if err := m.daemonSupportsTermOnly(); err != nil {
		return 0, err
	}
	oldPid, alive := m.processStatus(name)
	if !alive {
		return 0, fmt.Errorf("term-only restart requires live process %s; use start instead", name)
	}
	if err := m.beginTermStop(name, oldPid); err != nil {
		return 0, err
	}
	pgid, err := syscall.Getpgid(oldPid)
	if err != nil {
		m.abandonTermStop(name)
		return 0, fmt.Errorf("failed to signal process %s with pid %d: %w", name, oldPid, err)
	}
	if err := m.signalTermStopVerified(name, oldPid, pgid); err != nil {
		m.abandonTermStop(name)
		return 0, err
	}
	if !waitForProcessDeath(oldPid, grace) {
		m.markTermStopTimedOut(name)
		return 0, fmt.Errorf(
			"term-only restart of %s timed out after %s: pid %d is still running and has been left untouched; no replacement was started",
			name, grace, oldPid)
	}
	m.clearRuntimeIdentity(name)
	if err := ensurePortFree(def.Port, name); err != nil {
		m.abandonTermStop(name)
		return 0, err
	}
	pid, err := m.StartProcess(name)
	if err != nil {
		// The old process is gone; a replacement could not start. Release the
		// in-flight hold so crash supervision can try, and say so plainly.
		m.abandonTermStop(name)
		return 0, fmt.Errorf("old process %s exited but its replacement failed to start (supervision will resume): %w", name, err)
	}
	m.resetRestartAttempt(name)
	return pid, nil
}

// markTermStopTimedOut records the durable timeout marker under the state
// lock. UNCONDITIONALLY durable: if the old process happens to exit on its
// own in the instant between the grace deadline and this write, the marker is
// still recorded — a late natural exit is not operator resolution, and
// resuming supervision then would relaunch (or briefly supervise a corpse
// next to an operator's manual replacement). Only a successful new start
// (recordStarted) or a completed stop (resetRuntimeIdentity) releases it.
func (m *Manager) markTermStopTimedOut(name string) {
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.TermStopRequestedAt == nil {
			return false
		}
		p.TermStopTimedOut = true
		p.TermStopRequestedAt = nil
		p.TermStopOriginalPid = nil
		p.TermStopOriginalStartTime = nil
		return true
	})
}
