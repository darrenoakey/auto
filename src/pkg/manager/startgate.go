package manager

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// ErrStartsInhibited means this supervisor has begun stopping and cannot launch
// more services. The latch lasts for this Manager's lifetime, not across boots.
var ErrStartsInhibited = errors.New("auto is shutting down; service starts are inhibited")

// SystemShutdownRequested reads XNU's one-way committed shutdown flag. This is
// not SIGTERM, sleep, logout, or the GUI's cancellable shutdown request. Never
// write this sysctl: writing 1 commits kernel power management to shutdown.
func SystemShutdownRequested() (bool, error) {
	value, err := unix.SysctlUint32("kern.willshutdown")
	if err != nil {
		return false, fmt.Errorf("read kern.willshutdown: %w", err)
	}
	return value != 0, nil
}

// InhibitStarts irreversibly blocks new launches in this Manager, including
// retries inside an active watch tick. It is safe to call from a signal reader
// while another goroutine finishes recording an already-started child.
func (m *Manager) InhibitStarts() {
	m.stopping.Store(true)
}

// checkStartsAllowed fails closed if shutdown state cannot be determined. Each
// CLI process reads the same kernel flag, including a new watcher started by
// launchd during this boot; no file marker or reset-on-start race is needed.
func (m *Manager) checkStartsAllowed() error {
	if m.stopping.Load() {
		return ErrStartsInhibited
	}
	shutdown, err := SystemShutdownRequested()
	if err != nil {
		return fmt.Errorf("refusing service launch: %w", err)
	}
	if shutdown {
		m.InhibitStarts()
		return ErrStartsInhibited
	}
	return nil
}

// enterStart holds the spawn barrier until both the child and its ownership
// record are settled. ShutdownAll takes the exclusive side before collecting
// targets, so an in-flight spawn cannot be omitted from the teardown snapshot.
func (m *Manager) enterStart() error {
	m.startMu.RLock()
	if err := m.checkStartsAllowed(); err != nil {
		m.startMu.RUnlock()
		return err
	}
	return nil
}
