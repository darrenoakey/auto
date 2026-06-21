package manager

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentIncrementsNoLostUpdates is the direct regression test for the
// state-wipe bug: many goroutines mutating the same entry must serialize so every
// update lands and the command is never lost.
func TestConcurrentIncrementsNoLostUpdates(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.incrementRestartAttempt("svc")
		}()
	}
	wg.Wait()
	data := m.loadStateFile()
	if got := data.Processes["svc"].RestartAttempt; got != n {
		t.Fatalf("RestartAttempt = %d, want %d (lost updates indicate a race)", got, n)
	}
	if data.Processes["svc"].Command == "" {
		t.Fatal("command was wiped by concurrent writes")
	}
}

// TestConcurrentAddsAllSurvive verifies concurrent AddProcess calls all persist.
func TestConcurrentAddsAllSurvive(t *testing.T) {
	m := newTestManager(t)
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("svc%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.AddProcess(name, "sleep 1", nil, "")
		}()
	}
	wg.Wait()
	if got := len(m.definedNames()); got != n {
		t.Fatalf("defined = %d, want %d (concurrent adds were lost)", got, n)
	}
}

// TestMutateProcessIgnoresUnknownName ensures runtime mutations never recreate a
// command-less stub for a vanished name — the second guard against the wipe.
func TestMutateProcessIgnoresUnknownName(t *testing.T) {
	m := newTestManager(t)
	m.incrementRestartAttempt("ghost")
	m.recordStarted("ghost", 4242, "/tmp/x.log")
	data := m.loadStateFile()
	if _, ok := data.Processes["ghost"]; ok {
		t.Fatal("mutating an unknown name created a stub entry")
	}
}
