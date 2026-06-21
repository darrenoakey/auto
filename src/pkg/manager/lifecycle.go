package manager

import (
	"fmt"
	"syscall"
	"time"
)

// StartProcess launches a configured process, force-freeing its port first, and
// records its pid and start time for identity verification.
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
	pid, logPath, err := m.spawnWithRetry(name, def.Command, def.Workdir)
	if err != nil {
		return 0, err
	}
	m.recordStarted(name, pid, logPath)
	return pid, nil
}

// recordStarted writes the runtime fields of a freshly started process,
// preserving its restart bookkeeping. If the definition vanished mid-start
// (concurrently removed) it records nothing rather than recreating a stub.
func (m *Manager) recordStarted(name string, pid int, logPath string) {
	st := processStartTime(pid)
	m.mutateProcess(name, func(p *Process) {
		p.Pid = &pid
		p.StartTime = &st
		p.ExplicitlyStopped = false
		p.LogPath = logPath
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
func (m *Manager) StopProcess(name string, markExplicit bool) error {
	pid, alive := m.processStatus(name)
	if !alive {
		return fmt.Errorf("process %s is not running", name)
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to stop process %s with pid %d: %w", name, pid, err)
	}
	if err := m.escalateKill(name, pid, pgid); err != nil {
		return err
	}
	m.freePortAfterStop(name)
	if markExplicit {
		m.markExplicitlyStopped(name)
	}
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
