package manager

import "time"

// processStatus returns the live pid of a managed process, or (0, false) if it
// is not running. PID-reuse is defeated by matching the recorded start time.
func (m *Manager) processStatus(name string) (int, bool) {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok || p.Pid == nil {
		return 0, false
	}
	if isOurProcess(*p.Pid, p.StartTime) {
		return *p.Pid, true
	}
	return 0, false
}

// Status returns the live pid of a managed process and whether it is running.
func (m *Manager) Status(name string) (int, bool) {
	return m.processStatus(name)
}

// isExplicitlyStopped reports whether the user stopped the process via `stop`.
func (m *Manager) isExplicitlyStopped(name string) bool {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok {
		return false
	}
	return p.ExplicitlyStopped
}

// nowUnix returns the current time as a unix timestamp in seconds, matching the
// float timestamps stored by the original implementation.
func nowUnix() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
