package manager

import (
	"crypto/sha1"
	"encoding/binary"
	"time"
)

// restartJitter is a deterministic per-service offset (0..RestartJitterWindow-1
// seconds) so services whose backoff elapses together do not respawn in a
// synchronized burst. It is stable across processes (content hash, not a salted
// runtime hash).
func restartJitter(name string) int {
	if RestartJitterWindow <= 0 {
		return 0
	}
	sum := sha1.Sum([]byte(name))
	v := binary.BigEndian.Uint64(sum[:8])
	return int(v % uint64(RestartJitterWindow))
}

// maxBackoffExp is the largest exponent worth computing: 2^9 = 512s already
// exceeds MaxRestartBackoff (300s), so any higher attempt is simply capped. This
// bound also prevents the 1<<n * time.Second multiplication from overflowing
// int64, which previously wrapped to a tiny value and defeated the cap entirely.
const maxBackoffExp = 9

// restartBackoff returns the exponential backoff for a process based on its
// consecutive restart-attempt count, capped at MaxRestartBackoff.
func (m *Manager) restartBackoff(name string) time.Duration {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok {
		return time.Second
	}
	if p.RestartAttempt >= maxBackoffExp || p.RestartAttempt < 0 {
		return MaxRestartBackoff
	}
	backoff := time.Duration(1<<uint(p.RestartAttempt)) * time.Second
	if backoff > MaxRestartBackoff {
		return MaxRestartBackoff
	}
	return backoff
}

// incrementRestartAttempt bumps the restart counter and records the attempt time.
func (m *Manager) incrementRestartAttempt(name string) {
	m.mutateProcess(name, func(p *Process) {
		p.RestartAttempt++
		t := nowUnix()
		p.LastRestartTime = &t
	})
}

// resetRestartAttempt clears the restart counter after a successful start.
func (m *Manager) resetRestartAttempt(name string) {
	m.mutateProcess(name, func(p *Process) {
		p.RestartAttempt = 0
		p.LastRestartTime = nil
	})
}

// shouldRestart decides whether a dead, non-explicitly-stopped process is past
// its backoff window and may be restarted.
func (m *Manager) shouldRestart(name string) bool {
	if m.isExplicitlyStopped(name) {
		return false
	}
	if _, alive := m.processStatus(name); alive {
		return false
	}
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok || p.LastRestartTime == nil {
		return true
	}
	backoff := m.restartBackoff(name) + time.Duration(restartJitter(name))*time.Second
	elapsed := time.Duration((nowUnix() - *p.LastRestartTime) * float64(time.Second))
	return elapsed >= backoff
}

// checkAndResetBackoff resets the backoff once a process has been running stably
// past SuccessfulStartThreshold. It does a lock-free pre-check first so the common
// case (no outstanding backoff) takes no lock.
func (m *Manager) checkAndResetBackoff(name string) {
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok || p.RestartAttempt == 0 || p.LastRestartTime == nil {
		return
	}
	elapsed := time.Duration((nowUnix() - *p.LastRestartTime) * float64(time.Second))
	if elapsed < SuccessfulStartThreshold {
		return
	}
	m.mutateProcess(name, func(p *Process) {
		p.RestartAttempt = 0
		p.LastRestartTime = nil
	})
}
