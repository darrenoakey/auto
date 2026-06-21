// Package manager implements the auto daemon process manager: it stores process
// definitions and runtime state in a single state.json file, starts and stops
// managed processes, and supervises them with restart backoff.
package manager

import (
	"os"
	"path/filepath"
	"time"
)

// Timeouts and tuning constants for process supervision. These mirror the
// behaviour of the original Python implementation so existing state files and
// expectations carry over unchanged.
const (
	// SigtermTimeout is how long to wait after SIGTERM before escalating.
	SigtermTimeout = 5 * time.Second
	// SigkillTimeout is how long to wait after SIGKILL before giving up.
	SigkillTimeout = 5 * time.Second
	// SuccessfulStartThreshold is the uptime after which restart backoff resets.
	SuccessfulStartThreshold = 60 * time.Second
	// MaxRestartBackoff caps the exponential restart backoff. A transient host
	// spawn failure should not become a multi-hour outage, so the cap is minutes.
	MaxRestartBackoff = 300 * time.Second
	// RestartJitterWindow staggers services whose backoff elapses together so
	// they do not respawn in one synchronized burst.
	RestartJitterWindow = 30
	// StartAllSpawnStagger spaces successive spawns during a mass start.
	StartAllSpawnStagger = 200 * time.Millisecond
	// MaxRestartsPerWatchTick spreads a post-reboot mass start over a few ticks.
	MaxRestartsPerWatchTick = 5
	// SpawnRetryAttempts is how many times a transient spawn failure is retried.
	SpawnRetryAttempts = 5
	// SpawnRetryBaseDelay is multiplied by the attempt number between retries.
	SpawnRetryBaseDelay = 500 * time.Millisecond
	// SpawnVerifyDelay is the grace period to confirm a child survived its exec.
	SpawnVerifyDelay = 400 * time.Millisecond
)

// transientExecMarkers are substrings written to a child's log when its execve
// fails asynchronously under heavy host load. Popen-style spawning cannot report
// these, so the log is inspected to decide whether to retry.
var transientExecMarkers = []string{
	"Resource deadlock avoided",        // EDEADLK in the child's execve
	"Resource temporarily unavailable", // EAGAIN
	"Cannot allocate memory",           // ENOMEM
}

// Manager owns a single auto project tree (state file + logs) rooted at root.
// All operations are methods so tests can run against a temporary root.
type Manager struct {
	root string
}

// New returns a Manager rooted at the given project directory.
func New(root string) *Manager {
	return &Manager{root: root}
}

// Default returns a Manager rooted at the canonical ~/local/auto project tree.
func Default() *Manager {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/Users/darrenoakey"
	}
	return New(filepath.Join(home, "local", "auto"))
}

// Root returns the project root directory.
func (m *Manager) Root() string {
	return m.root
}

// statePath returns the path to the single state file.
func (m *Manager) statePath() string {
	return filepath.Join(m.root, "local", "state.json")
}

// logDir returns the directory where process logs are stored, creating it.
func (m *Manager) logDir() string {
	dir := filepath.Join(m.root, "output", "logs")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
