package manager

import (
	"fmt"
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

// BenchmarkWatchTickIdleRegistry measures real state-file supervision without
// spawning services: explicitly stopped registrations must stay cheap at scale.
func BenchmarkWatchTickIdleRegistry(b *testing.B) {
	for _, count := range []int{1, 50, 150} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := New(b.TempDir())
			b.Cleanup(m.WaitForLogArchive)
			seedIdleRegistry(m, count)
			m.WatchTick()
			m.WaitForLogArchive()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.WatchTick()
			}
		})
	}
}

func seedIdleRegistry(m *Manager, count int) {
	m.withState(func(data *stateFile) bool {
		for i := 0; i < count; i++ {
			data.Processes[fmt.Sprintf("stopped-%03d", i)] = &Process{
				Command: "sleep 300", Workdir: m.Root(), ExplicitlyStopped: true,
			}
		}
		return true
	})
}

func TestWatchTickIdleRegistryOnlyWritesHeartbeat(t *testing.T) {
	m := newTestManager(t)
	seedIdleRegistry(m, 150)
	before := m.stateCacheGen
	m.WatchTick()
	if writes := m.stateCacheGen - before; writes != 1 {
		t.Fatalf("idle tick rewrote the entire registry %d times; want only heartbeat", writes)
	}
}

func TestWatchTickIdleRegistryAllocationBudget(t *testing.T) {
	m := newTestManager(t)
	seedIdleRegistry(m, 150)
	m.WatchTick()
	m.WaitForLogArchive()
	// The real disk-backed tick uses about 3,400 allocations. Whole-registry
	// parsing per service used 156,000; retain headroom without hiding that bug.
	if allocations := testing.AllocsPerRun(3, m.WatchTick); allocations > 10000 {
		t.Fatalf("idle tick allocated %.0f times; must not parse the registry per service", allocations)
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
