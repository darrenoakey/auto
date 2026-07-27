package manager

import (
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

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

func TestStopProcessDeadRegisteredServicePersistsExplicitStop(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	pid, err := m.StartProcess("sleeper")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	killGroup(t, pid)
	waitForActualProcessDeath(t, pid)

	if err := m.StopProcess("sleeper", true); err != nil {
		t.Fatalf("stop dead registered service: %v", err)
	}
	reopened := New(m.Root())
	process := reopened.loadStateFile().Processes["sleeper"]
	if process == nil || !process.ExplicitlyStopped {
		t.Fatal("explicit stop did not persist after reopening manager")
	}
	if process.Pid != nil || process.StartTime != nil || process.StartingSince != nil {
		t.Fatalf("stale runtime identity persisted: %+v", process)
	}
	if results := reopened.RestartDead(); len(results) != 0 {
		t.Fatalf("RestartDead respawned explicitly stopped service: %v", results)
	}
	if _, alive := reopened.Status("sleeper"); alive {
		t.Fatal("explicitly stopped service should remain dead")
	}
}

func TestStopProcessUnknownFailsWithoutCreatingState(t *testing.T) {
	m := newTestManager(t)
	if err := m.StopProcess("ghost", true); err == nil {
		t.Fatal("stopping an unknown process should fail")
	}
	if _, exists := m.loadStateFile().Processes["ghost"]; exists {
		t.Fatal("stopping an unknown process created a state entry")
	}
}

// waitForActualProcessDeath synchronizes on the real process table without
// fixed-duration sleeps.
func waitForActualProcessDeath(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(SigkillTimeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	t.Cleanup(func() { deadline.Stop() })
	t.Cleanup(func() { ticker.Stop() })
	for {
		select {
		case <-ticker.C:
			if !isProcessAlive(pid) {
				return
			}
		case <-deadline.C:
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("pid %d did not die", pid)
		}
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

// TestStopProcessMarksExplicitBeforeKilling proves the TOCTOU fix directly: the
// explicitly-stopped flag must already be true by the time any observer can see
// the process as dead, so a concurrent WatchTick can never read the state as
// "dead and not explicitly stopped" and race a competing respawn into the gap
// while this call is still tearing the old instance down.
func TestStopProcessMarksExplicitBeforeKilling(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "sleeper", "sleep 300", nil)
	pid, err := m.StartProcess("sleeper")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("sleeper", true) })

	done := make(chan bool)
	go func() {
		for isProcessAlive(pid) {
			time.Sleep(time.Millisecond)
		}
		// pid just went dead: the explicit-stop flag must already be set.
		done <- m.isExplicitlyStopped("sleeper")
	}()

	if err := m.StopProcess("sleeper", true); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if flagWasSet := <-done; !flagWasSet {
		t.Fatal("process was observed dead before the explicitly-stopped flag was set — TOCTOU window for a concurrent WatchTick")
	}
}

// TestRestartProcessNotRacedByWatchTick drives WatchTick concurrently with a
// RestartProcess call and asserts the service converges to exactly one live
// instance holding its port, with no competing instance left running.
func TestRestartProcessNotRacedByWatchTick(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc unavailable")
	}
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	mustAdd(t, m, "holder", "exec nc -l "+strconv.Itoa(port), &port)
	first, err := m.StartProcess("holder")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Skip("nc did not bind the port (variant differs)")
	}

	stop := make(chan struct{})
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		for {
			select {
			case <-stop:
				return
			default:
				m.WatchTick()
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	second, err := m.RestartProcess("holder")
	close(stop)
	<-tickDone
	t.Cleanup(func() { _ = m.StopProcess("holder", true) })
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if second == first {
		t.Fatalf("restart should yield a new pid, both %d", first)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("port should be held after restart")
	}
	holders := lsofPortPids(port)
	if len(holders) != 1 || holders[0] != second {
		t.Fatalf("expected exactly pid %d holding port %d, got %v", second, port, holders)
	}
	if pid, alive := m.Status("holder"); !alive || pid != second {
		t.Fatalf("state should track the single survivor, got (%d,%v)", pid, alive)
	}
}
