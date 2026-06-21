package manager

import (
	"os"
	"path/filepath"
	"syscall"
)

// withState runs a load-modify-save transaction under an exclusive advisory file
// lock so concurrent auto processes (the watch daemon plus any CLI invocation)
// can never interleave their read-modify-write cycles. Without this, two writers
// could each load, mutate, and save in turn, dropping the other's entries — the
// race that previously wiped the state file. mutate reports whether it changed
// anything; the file is rewritten only then.
//
// withState must never be called from inside another withState on the same
// Manager: the lock is not reentrant and would deadlock. Mutating methods keep
// their transaction a leaf (pure in-memory edits, no nested locking calls).
func (m *Manager) withState(mutate func(*stateFile) bool) {
	unlock := m.lockState()
	defer unlock()
	data := m.loadStateFile()
	if mutate(data) {
		m.saveStateFile(data)
	}
}

// lockState acquires the exclusive state lock and returns a release function. The
// lock lives on a dedicated file that is never renamed or removed, so the lock
// identity is stable across the atomic state-file rewrites. On any failure it
// degrades to a no-op rather than blocking a critical daemon.
func (m *Manager) lockState() func() {
	path := filepath.Join(filepath.Dir(m.statePath()), "state.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// mutateProcess applies fn to an existing process entry under the state lock,
// doing nothing if the name is absent. It never creates a command-less stub —
// the second safeguard against the wipe, where stub entries for vanished names
// once overwrote real definitions.
func (m *Manager) mutateProcess(name string, fn func(*Process)) {
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok {
			return false
		}
		fn(p)
		return true
	})
}
