package manager

import (
	"fmt"
	"time"
)

// WatchTick performs one supervision pass: it resets backoff and applies periodic
// restarts for running processes, and restarts dead ones that are past their
// backoff window. Fresh starts are rate-limited per tick so a post-reboot mass
// start does not fire every spawn in a single instant.
func (m *Manager) WatchTick() {
	restarts := 0
	for _, name := range m.definedNames() {
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
