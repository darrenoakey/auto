package manager

import (
	"fmt"
	"os"
	"time"
)

// WatchTick performs one supervision pass: it fulfills any delegated spawn
// requests left by external CLI invocations (see delegateSpawnToDaemon in
// lifecycle.go — only the daemon's own process may fork/exec a managed
// service), resets backoff and applies periodic restarts for running
// processes, and restarts dead ones that are past their backoff window.
// Fresh crash-restarts are rate-limited per tick so a post-reboot mass start
// does not fire every spawn in a single instant; a fulfilled spawn request
// counts toward the same per-tick cap since it performs an identical spawn.
func (m *Manager) WatchTick() {
	m.recordHeartbeat()
	m.maybeArchiveOldLogs()
	// One process-table snapshot serves every processStatus call in this tick.
	// Without it each managed service cost two `ps` forks per tick (state +
	// lstart), so ~40 services meant ~80 fork+execs every second.
	m.setProcSnapshot(newProcTable())
	defer m.setProcSnapshot(nil)
	restarts := 0
	for _, name := range m.definedNames() {
		if restarts < MaxRestartsPerWatchTick && m.fulfillSpawnRequest(name) {
			restarts++
			continue
		}
		if _, alive := m.processStatus(name); alive {
			m.superviseRunning(name)
			continue
		}
		if restarts >= MaxRestartsPerWatchTick || !m.shouldRestart(name) {
			continue
		}
		if m.restartDead(name) {
			restarts++
		}
	}
}

// recordHeartbeat marks this process as the live watch daemon for this state
// file. liveDaemon (lifecycle.go) reads it to decide whether StartProcess
// should delegate a spawn here rather than forking directly from an external
// CLI caller.
func (m *Manager) recordHeartbeat() {
	m.withState(func(data *stateFile) bool {
		now := nowUnix()
		pid := os.Getpid()
		data.DaemonHeartbeat = &now
		data.DaemonPid = &pid
		return true
	})
}

// fulfillSpawnRequest performs a pending delegated spawn request for name, if
// one is live. It is the daemon-side half of delegateSpawnToDaemon: only the
// watch daemon's own process calls this, so the resulting child always
// inherits Auto.app's macOS responsible-process identity regardless of which
// external CLI invocation asked for it. Returns true if it performed a spawn
// this tick (the caller should skip normal dead-process supervision for name
// in the same tick, since this already resolved it).
func (m *Manager) fulfillSpawnRequest(name string) bool {
	def, ok := m.definition(name)
	if !ok || def.SpawnRequestedAt == nil {
		return false
	}
	if !isSpawnRequestLive(def) {
		// Abandoned: the requester gave up (or died) before we reached it.
		m.clearSpawnRequest(name)
		return false
	}
	if _, alive := m.processStatus(name); alive {
		// Already satisfied by a race (e.g. a crash-restart beat the delegated
		// request to it): nothing left to spawn.
		m.clearSpawnRequest(name)
		return false
	}
	pid, err := m.spawnDirect(name, def)
	m.clearSpawnRequest(name)
	if err != nil {
		fmt.Printf("Failed to fulfill delegated spawn request for %s: %v\n", name, err)
		return false
	}
	m.resetRestartAttempt(name)
	fmt.Printf("Fulfilled delegated spawn request for %s with pid %d\n", name, pid)
	return true
}

// superviseRunning maintains a running process: resets backoff once stable and
// applies a periodic restart if one is due.
func (m *Manager) superviseRunning(name string) {
	m.checkAndResetBackoff(name)
	if !m.needsPeriodicRestart(name) {
		return
	}
	interval := 0
	if iv := m.GetRestartInterval(name); iv != nil {
		interval = *iv
	}
	pid, err := m.performPeriodicRestart(name)
	if err != nil {
		fmt.Printf("Failed periodic restart of %s: %v\n", name, err)
		return
	}
	fmt.Printf("Periodic restart of %s (every %s) with pid %d\n", name, FormatInterval(interval), pid)
}

// restartDead attempts to restart one dead process, recording the attempt for
// backoff. Returns whether a fresh start was performed.
func (m *Manager) restartDead(name string) bool {
	m.incrementRestartAttempt(name)
	pid, err := m.StartProcess(name)
	if err != nil {
		fmt.Printf("Failed to restart %s: %v\n", name, err)
		return false
	}
	fmt.Printf("Restarted %s with pid %d after %s backoff\n", name, pid, m.restartBackoff(name))
	return true
}

// StartAll starts every configured process that is not already running, with a
// small stagger so the boot-time mass start does not fire every fork at once.
func (m *Manager) StartAll() {
	started := false
	for _, name := range m.definedNames() {
		if _, alive := m.processStatus(name); alive {
			continue
		}
		if started && StartAllSpawnStagger > 0 {
			time.Sleep(StartAllSpawnStagger)
		}
		started = true
		if _, err := m.StartProcess(name); err != nil {
			fmt.Printf("Failed to start %s: %v\n", name, err)
		}
	}
}

// RestartDead restarts all dead, non-explicitly-stopped processes, force-freeing
// ports. It returns a map of name to the new pid or an error message.
func (m *Manager) RestartDead() map[string]string {
	results := make(map[string]string)
	for _, name := range m.definedNames() {
		if _, alive := m.processStatus(name); alive || m.isExplicitlyStopped(name) {
			continue
		}
		if pid, err := m.StartProcess(name); err != nil {
			results[name] = err.Error()
		} else {
			results[name] = fmt.Sprintf("pid %d", pid)
		}
	}
	return results
}
