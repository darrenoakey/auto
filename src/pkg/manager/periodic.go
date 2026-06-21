package manager

import (
	"fmt"
	"strconv"
	"strings"
)

// intervalSuffixes maps human-readable interval suffixes to seconds.
var intervalSuffixes = map[byte]int{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}

// ParseInterval converts a human-readable interval like "24h", "30m", "7d", or a
// bare seconds count into seconds.
func ParseInterval(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty interval string")
	}
	if mult, ok := intervalSuffixes[s[len(s)-1]]; ok {
		value, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid interval: %s", s)
		}
		return int(value * float64(mult)), nil
	}
	value, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid interval: %s. Use e.g. 30m, 12h, 1d", s)
	}
	return value, nil
}

// FormatInterval renders seconds as a compact human-readable string.
func FormatInterval(seconds int) string {
	switch {
	case seconds >= 86400 && seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400)
	case seconds >= 3600 && seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds >= 60 && seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// SetRestartInterval configures (or clears, with nil) a periodic restart interval.
func (m *Manager) SetRestartInterval(name string, seconds *int) error {
	found := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok {
			return false
		}
		found = true
		applyInterval(p, seconds)
		return true
	})
	if !found {
		return fmt.Errorf("process %s not found", name)
	}
	return nil
}

// applyInterval sets or clears a process's periodic restart fields.
func applyInterval(p *Process, seconds *int) {
	if seconds != nil {
		p.RestartIntervalSeconds = seconds
		if p.LastPeriodicRestart == nil {
			t := nowUnix()
			p.LastPeriodicRestart = &t
		}
		return
	}
	p.RestartIntervalSeconds = nil
	p.LastPeriodicRestart = nil
}

// GetRestartInterval returns the periodic restart interval in seconds, or nil.
func (m *Manager) GetRestartInterval(name string) *int {
	data := m.loadStateFile()
	if p, ok := data.Processes[name]; ok {
		return p.RestartIntervalSeconds
	}
	return nil
}

// needsPeriodicRestart reports whether a running process is due for a scheduled
// restart.
func (m *Manager) needsPeriodicRestart(name string) bool {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok || p.RestartIntervalSeconds == nil || *p.RestartIntervalSeconds == 0 || p.Pid == nil {
		return false
	}
	baseline := periodicBaseline(p)
	if baseline == nil {
		return false
	}
	return (nowUnix() - *baseline) >= float64(*p.RestartIntervalSeconds)
}

// periodicBaseline returns the timestamp from which the next periodic restart is
// measured: the last periodic restart, or the process start time as a fallback.
func periodicBaseline(p *Process) *float64 {
	if p.LastPeriodicRestart != nil {
		return p.LastPeriodicRestart
	}
	if p.StartTime != nil {
		if dt, ok := parseLstartTime(*p.StartTime); ok {
			ts := float64(dt.Unix())
			return &ts
		}
	}
	return nil
}

// performPeriodicRestart restarts a process for scheduled maintenance and
// records the restart time. It uses markExplicit=false since the watch loop
// owns the operation and cannot race itself.
func (m *Manager) performPeriodicRestart(name string) (int, error) {
	def, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if pid, alive := m.processStatus(name); alive {
		if err := m.obliterateProcess(name, pid, false); err != nil {
			return 0, err
		}
	}
	if err := ensurePortFree(def.Port, name); err != nil {
		return 0, err
	}
	pid, err := m.StartProcess(name)
	if err != nil {
		return 0, err
	}
	m.recordPeriodicRestart(name)
	return pid, nil
}

// recordPeriodicRestart stamps the last periodic restart time.
func (m *Manager) recordPeriodicRestart(name string) {
	m.mutateProcess(name, func(p *Process) {
		t := nowUnix()
		p.LastPeriodicRestart = &t
	})
}
