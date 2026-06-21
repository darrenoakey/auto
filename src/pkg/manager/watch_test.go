package manager

import (
	"syscall"
	"testing"
)

func TestWatchTickRestartsDeadProcess(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	first, err := m.StartProcess("sleeper")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	killGroup(t, first)
	if !waitForProcessDeath(first, SigtermTimeout) {
		t.Fatalf("pid %d did not die", first)
	}
	m.WatchTick()
	second, alive := m.Status("sleeper")
	t.Cleanup(func() { _ = m.StopProcess("sleeper", true) })
	if !alive || second == first {
		t.Fatalf("WatchTick should have restarted with a new pid; got (%d,%v) old %d", second, alive, first)
	}
}

func TestStartAllStartsEverything(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "a", "sleep 300", nil)
	mustAdd(t, m, "b", "sleep 300", nil)
	m.StartAll()
	t.Cleanup(func() {
		_ = m.StopProcess("a", true)
		_ = m.StopProcess("b", true)
	})
	if _, alive := m.Status("a"); !alive {
		t.Fatal("a should be running")
	}
	if _, alive := m.Status("b"); !alive {
		t.Fatal("b should be running")
	}
}

func TestRestartDeadSkipsExplicitlyStopped(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "live", "sleep 300", nil)
	mustAdd(t, m, "halted", "sleep 300", nil)
	m.markExplicitlyStopped("halted")
	results := m.RestartDead()
	t.Cleanup(func() { _ = m.StopProcess("live", true) })
	if _, ok := results["halted"]; ok {
		t.Fatalf("explicitly stopped process should be skipped, got %v", results)
	}
	if _, ok := results["live"]; !ok {
		t.Fatalf("dead process should be restarted, got %v", results)
	}
}

// killGroup SIGKILLs the process group of pid for test teardown of a child.
func killGroup(t *testing.T, pid int) {
	t.Helper()
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
