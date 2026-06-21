package manager

import (
	"testing"
	"time"
)

func TestRestartJitterDeterministicInRange(t *testing.T) {
	for _, name := range []string{"alpha", "beta", "gamma", "service-x"} {
		first := restartJitter(name)
		if first != restartJitter(name) {
			t.Fatalf("jitter for %q not deterministic", name)
		}
		if first < 0 || first >= RestartJitterWindow {
			t.Fatalf("jitter for %q = %d, out of [0,%d)", name, first, RestartJitterWindow)
		}
	}
}

func TestRestartBackoffExponentialAndCapped(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{3, 8 * time.Second},
		{8, 256 * time.Second},
		{9, MaxRestartBackoff},
		{20, MaxRestartBackoff},
		// Regression: a huge historical attempt count must stay capped, not
		// overflow 1<<n * time.Second into a tiny value that defeats the cap.
		{1375, MaxRestartBackoff},
		{62, MaxRestartBackoff},
	}
	for _, tt := range tests {
		setRuntime(t, m, "svc", func(p *Process) { p.RestartAttempt = tt.attempt })
		if got := m.restartBackoff("svc"); got != tt.want {
			t.Errorf("attempt %d: backoff = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestShouldRestartRespectsStateAndBackoff(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)

	if !m.shouldRestart("svc") {
		t.Fatal("dead, never-attempted process should restart")
	}
	setRuntime(t, m, "svc", func(p *Process) { p.ExplicitlyStopped = true })
	if m.shouldRestart("svc") {
		t.Fatal("explicitly stopped process must not restart")
	}
	setRuntime(t, m, "svc", func(p *Process) {
		p.ExplicitlyStopped = false
		p.RestartAttempt = 5
		now := nowUnix()
		p.LastRestartTime = &now
	})
	if m.shouldRestart("svc") {
		t.Fatal("recent restart within backoff must not restart")
	}
	setRuntime(t, m, "svc", func(p *Process) {
		old := nowUnix() - 100000
		p.LastRestartTime = &old
	})
	if !m.shouldRestart("svc") {
		t.Fatal("backoff long elapsed should allow restart")
	}
}

func TestCheckAndResetBackoffClearsAfterStability(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	setRuntime(t, m, "svc", func(p *Process) {
		p.RestartAttempt = 4
		old := nowUnix() - float64(SuccessfulStartThreshold/time.Second) - 5
		p.LastRestartTime = &old
	})
	m.checkAndResetBackoff("svc")
	data := m.loadStateFile()
	if data.Processes["svc"].RestartAttempt != 0 || data.Processes["svc"].LastRestartTime != nil {
		t.Fatalf("backoff not reset: %+v", data.Processes["svc"])
	}
}

// setRuntime mutates a process's stored fields in the test state file.
func setRuntime(t *testing.T, m *Manager, name string, mutate func(*Process)) {
	t.Helper()
	data := m.loadStateFile()
	p, ok := data.Processes[name]
	if !ok {
		t.Fatalf("process %q not found", name)
	}
	mutate(p)
	m.saveStateFile(data)
}
