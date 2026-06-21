package manager

import "testing"

func TestStartAndStopProcess(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	pid, err := m.StartProcess("sleeper")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("sleeper", true) })
	if !isProcessAlive(pid) {
		t.Fatalf("pid %d should be alive after start", pid)
	}
	gotPid, alive := m.Status("sleeper")
	if !alive || gotPid != pid {
		t.Fatalf("Status = (%d,%v), want (%d,true)", gotPid, alive, pid)
	}
	if err := m.StopProcess("sleeper", true); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if isProcessAlive(pid) {
		t.Fatalf("pid %d should be dead after stop", pid)
	}
	if !m.isExplicitlyStopped("sleeper") {
		t.Fatal("should be marked explicitly stopped")
	}
}

func TestStartProcessAlreadyRunningFails(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	if _, err := m.StartProcess("sleeper"); err != nil {
		t.Fatalf("first start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("sleeper", true) })
	if _, err := m.StartProcess("sleeper"); err == nil {
		t.Fatal("starting an already-running process should fail")
	}
}

func TestStartProcessUnknownFails(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.StartProcess("ghost"); err == nil {
		t.Fatal("starting an unknown process should fail")
	}
}

func TestStopProcessNotRunningFails(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	if err := m.StopProcess("sleeper", true); err == nil {
		t.Fatal("stopping a non-running process should fail")
	}
}

func TestRestartProcessGivesNewPid(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	first, err := m.StartProcess("sleeper")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := m.RestartProcess("sleeper")
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("sleeper", true) })
	if second == first {
		t.Fatalf("restart should yield a new pid, both %d", first)
	}
	if isProcessAlive(first) {
		t.Fatalf("old pid %d should be dead", first)
	}
	if !isProcessAlive(second) {
		t.Fatalf("new pid %d should be alive", second)
	}
	if m.isExplicitlyStopped("sleeper") {
		t.Fatal("restarted process must not be marked explicitly stopped")
	}
}
