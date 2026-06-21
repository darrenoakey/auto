package manager

import "testing"

func TestProcessStatusDeadWhenNeverStarted(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if pid, alive := m.processStatus("svc"); alive || pid != 0 {
		t.Fatalf("unstarted process should be dead, got pid=%d alive=%v", pid, alive)
	}
}

func TestIsExplicitlyStopped(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if m.isExplicitlyStopped("svc") {
		t.Fatal("new process should not be explicitly stopped")
	}
	m.markExplicitlyStopped("svc")
	if !m.isExplicitlyStopped("svc") {
		t.Fatal("should be explicitly stopped after marking")
	}
	m.clearExplicitStop("svc")
	if m.isExplicitlyStopped("svc") {
		t.Fatal("should be cleared")
	}
}

func TestNowUnixPositive(t *testing.T) {
	if nowUnix() <= 0 {
		t.Fatal("nowUnix should be positive")
	}
}
