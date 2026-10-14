package manager

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cleanupManagedProcess(t *testing.T, manager *Manager, name string) {
	t.Helper()
	t.Cleanup(func() {
		if pid, alive := manager.Status(name); alive {
			killGroup(t, pid)
		}
	})
}

func TestIdleStrictOverlapLeavesRegistryAndBackupUntouched(t *testing.T) {
	m := newTestManager(t)
	seedIdleRegistry(m, 150)
	m.recordHeartbeat()
	paths := []string{m.statePath(), backupPath(m.statePath())}
	before := strictOverlapFileStats(t, paths)
	cached := m.loadStateFile()
	for _, name := range m.definedNames() {
		if m.fulfillStrictOverlapSpawnRequest(name) {
			t.Fatalf("unexpected spawn for stopped service %s", name)
		}
	}
	after := strictOverlapFileStats(t, paths)
	for i := range paths {
		if !os.SameFile(before[i], after[i]) || !before[i].ModTime().Equal(after[i].ModTime()) {
			t.Fatalf("idle strict overlap rewrote %s", paths[i])
		}
	}
	if m.loadStateFile() != cached {
		t.Fatal("idle strict overlap invalidated the parsed registry cache")
	}
}

func strictOverlapFileStats(t *testing.T, paths []string) []os.FileInfo {
	t.Helper()
	var stats []os.FileInfo
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stats = append(stats, info)
	}
	return stats
}

func TestStrictOverlapExpiryRechecksExternalStateAndOnlyWritesChanges(t *testing.T) {
	for _, state := range []string{"absent", "live", "claimed", "candidate", "result", "error", "expired", "removed"} {
		t.Run(state, func(t *testing.T) { verifyStrictOverlapExpiry(t, state) })
	}
}

func verifyStrictOverlapExpiry(t *testing.T, state string) {
	t.Helper()
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	m.loadStateFile() // warm a no-request snapshot before another CLI writes
	external := New(m.Root())
	external.withState(func(data *stateFile) bool {
		setExpiryTestState(data.Processes["svc"], state)
		if state == "removed" {
			delete(data.Processes, "svc")
		}
		return true
	})
	before := m.stateCacheGen
	m.discardExpiredStrictOverlapRequest("svc")
	wantWrites := int64(0)
	if state == "expired" {
		wantWrites = 1
	}
	if writes := m.stateCacheGen - before; writes != wantWrites {
		t.Fatalf("%s: got %d writes, want %d", state, writes, wantWrites)
	}
	verifyExpiryStoredState(t, m, state)
}

func setExpiryTestState(p *Process, state string) {
	if state == "absent" || state == "removed" {
		return
	}
	now, deadline := nowUnix(), nowUnix()-1
	p.StrictOverlapRequestId, p.StrictOverlapRequestedAt, p.StrictOverlapDeadline = state, &now, &deadline
	switch state {
	case "live":
		deadline += 60
	case "claimed":
		p.StrictOverlapClaimed = true
	case "candidate":
		p.StrictOverlapCandidatePid = intPtr(os.Getpid())
	case "result":
		p.StrictOverlapResultPid = intPtr(os.Getpid())
	case "error":
		p.StrictOverlapError = "replacement failed"
	}
}

func verifyExpiryStoredState(t *testing.T, m *Manager, state string) {
	t.Helper()
	p, ok := m.definition("svc")
	if state == "removed" {
		if ok {
			t.Fatal("removed service was resurrected")
		}
		return
	}
	want := state
	if state == "absent" || state == "expired" {
		want = ""
	}
	if !ok || p.StrictOverlapRequestId != want {
		t.Fatalf("%s request was lost or not cleared: %+v", state, p)
	}
}

func TestRestartProcessStrictOverlapBindFailurePreservesOldRuntime(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, false)
	cleanupManagedProcess(t, manager, "tunnel")
	waitForServing(t, port)
	original, _ := manager.definition("tunnel")
	setHeartbeat(t, manager, os.Getpid(), 0)

	if _, err := manager.RestartProcessStrictOverlap("tunnel"); err == nil {
		t.Fatal("strict overlap must fail when the old listener does not allow reuse")
	} else if !strings.Contains(err.Error(), "replacement bind failed with address already in use") {
		t.Fatalf("strict overlap did not reach the replacement bind failure: %v", err)
	}
	current, _ := manager.definition("tunnel")
	if current.Pid == nil || *current.Pid != oldPid || current.StartTime == nil || *current.StartTime != *original.StartTime || current.LogPath != original.LogPath {
		t.Fatalf("runtime metadata changed after failed strict overlap: original=%+v current=%+v", original, current)
	}
	if !isProcessAlive(oldPid) {
		t.Fatal("failed strict overlap terminated the old process")
	}
	waitForServing(t, port)
}

func TestRestartProcessStrictOverlapWithoutLiveWatcherLeavesOldRuntimeUntouched(t *testing.T) {
	for _, test := range []struct {
		name      string
		heartbeat bool
	}{
		{name: "absent"},
		{name: "stale", heartbeat: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := newTestManager(t)
			port := freePort(t)
			oldPid := startHolder(t, manager, "tunnel", port, true)
			cleanupManagedProcess(t, manager, "tunnel")
			waitForServing(t, port)
			if test.heartbeat {
				setHeartbeat(t, manager, os.Getpid(), DaemonHeartbeatTTL+time.Second)
			}

			if _, err := manager.RestartProcessStrictOverlap("tunnel"); err == nil {
				t.Fatal("strict overlap must fail without a live watcher")
			} else if !strings.Contains(err.Error(), "requires a live watch daemon") {
				t.Fatalf("strict overlap returned the wrong failure: %v", err)
			}
			definition, _ := manager.definition("tunnel")
			if definition.StrictOverlapRequestId != "" || definition.StrictOverlapClaimed ||
				definition.StrictOverlapCandidatePid != nil || definition.StrictOverlapResultPid != nil {
				t.Fatalf("watcherless strict overlap left pending or candidate state: %+v", definition)
			}
			if pid, alive := manager.Status("tunnel"); !alive || pid != oldPid {
				t.Fatalf("watcherless strict overlap changed old runtime: status=(%d,%v), old=%d", pid, alive, oldPid)
			}
			waitForServing(t, port)
		})
	}
}

func TestRestartProcessStrictOverlapDeadProcessDoesNotSpawn(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	killGroup(t, oldPid)
	waitForActualProcessDeath(t, oldPid)

	if _, err := manager.RestartProcessStrictOverlap("tunnel"); err == nil {
		t.Fatal("strict overlap must reject a dead old process")
	}
	if pid, alive := manager.Status("tunnel"); alive || pid != 0 {
		t.Fatalf("strict overlap spawned from a dead process: status=(%d,%v)", pid, alive)
	}
	if !isPortFree(port) {
		t.Fatal("strict overlap of a dead process unexpectedly created a listener")
	}
}

func TestStrictOverlapDelegationTimeoutCancelsLateSpawn(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	cleanupManagedProcess(t, manager, "tunnel")
	setHeartbeat(t, manager, os.Getpid()+765432, 0)

	if _, err := manager.requestStrictOverlap("tunnel", oldPid, 250*time.Millisecond); err == nil {
		t.Fatal("unfulfilled delegated strict overlap must time out")
	}
	definition, _ := manager.definition("tunnel")
	if definition.StrictOverlapRequestId != "" || definition.StrictOverlapClaimed {
		t.Fatalf("timed-out request was not cancelled: %+v", definition)
	}
	manager.WatchTick()
	time.Sleep(SpawnVerifyDelay + 100*time.Millisecond)
	if pid, alive := manager.Status("tunnel"); !alive || pid != oldPid {
		t.Fatalf("late watcher tick replaced old pid after timeout: status=(%d,%v), old=%d", pid, alive, oldPid)
	}
}

func TestStrictOverlapTimeoutCancelsClaimedCandidate(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	cleanupManagedProcess(t, manager, "tunnel")
	delayedCommand := fmt.Sprintf("sh -c 'sleep 1; exec %s -reuse -port %d'", helperBin, port)
	if err := manager.UpdateProcess("tunnel", &delayedCommand, nil, nil); err != nil {
		t.Fatalf("update delayed command: %v", err)
	}
	setHeartbeat(t, manager, os.Getpid()+543210, 0)

	type requestResult struct {
		pid int
		err error
	}
	requestDone := make(chan requestResult, 1)
	go func() {
		pid, err := manager.requestStrictOverlap("tunnel", oldPid, 100*time.Millisecond)
		requestDone <- requestResult{pid: pid, err: err}
	}()
	waitForStrictOverlapRequest(t, manager, "tunnel")
	fulfillDone := make(chan bool, 1)
	go func() { fulfillDone <- manager.fulfillStrictOverlapSpawnRequest("tunnel") }()
	waitForStrictOverlapClaim(t, manager, "tunnel")

	result := <-requestDone
	if result.err == nil || result.pid != 0 {
		t.Fatalf("claimed request timeout = (%d,%v), want failure", result.pid, result.err)
	}
	if fulfilled := <-fulfillDone; fulfilled {
		t.Fatal("cancelled claimed candidate must not be finalized")
	}
	definition, _ := manager.definition("tunnel")
	if definition.StrictOverlapRequestId != "" || definition.StrictOverlapClaimed || definition.StrictOverlapCancelled {
		t.Fatalf("cancelled claimed request was not cleaned up: %+v", definition)
	}
	if pid, alive := manager.Status("tunnel"); !alive || pid != oldPid {
		t.Fatalf("claimed timeout changed runtime ownership: status=(%d,%v), old=%d", pid, alive, oldPid)
	}
	time.Sleep(time.Second)
	if pid, alive := manager.Status("tunnel"); !alive || pid != oldPid {
		t.Fatalf("cancelled candidate appeared later: status=(%d,%v), old=%d", pid, alive, oldPid)
	}
}

func TestStrictOverlapTimeoutSettlementAcceptsFinalizedCandidate(t *testing.T) {
	manager, oldPid, candidatePid, requestId, startTime, logPath := prepareClaimedStrictOverlapCandidate(t)
	if !manager.finalizeStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime, logPath) {
		t.Fatal("candidate did not finalize")
	}

	pid, err, done := manager.settleStrictOverlapRequest("tunnel", requestId, true)
	if !done || err != nil || pid != candidatePid {
		t.Fatalf("timeout settlement = (%d,%v,%v), want finalized pid %d", pid, err, done, candidatePid)
	}
	definition, _ := manager.definition("tunnel")
	if definition.Pid == nil || *definition.Pid != candidatePid || definition.StrictOverlapRequestId != "" {
		t.Fatalf("settlement erased finalized candidate: %+v", definition)
	}
	if !isProcessAlive(oldPid) {
		t.Fatal("settling candidate result unexpectedly stopped original")
	}
}

func TestStrictOverlapExpiredClaimCannotBeOverwritten(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	cleanupManagedProcess(t, manager, "tunnel")
	if err := manager.registerStrictOverlapRequest("tunnel", "first", oldPid, time.Second); err != nil {
		t.Fatalf("register first request: %v", err)
	}
	if _, _, claimed := manager.claimStrictOverlapRequest("tunnel"); !claimed {
		t.Fatal("first request was not claimed")
	}
	manager.mutateProcess("tunnel", func(process *Process) {
		expired := nowUnix() - 1
		process.StrictOverlapDeadline = &expired
	})

	if err := manager.registerStrictOverlapRequest("tunnel", "second", oldPid, time.Second); err == nil {
		t.Fatal("expired claimed request was overwritten")
	}
	definition, _ := manager.definition("tunnel")
	if definition.StrictOverlapRequestId != "first" || !definition.StrictOverlapClaimed {
		t.Fatalf("expired claim ownership changed: %+v", definition)
	}
}

func TestStrictOverlapClaimRejectsChangedOriginalIdentity(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	t.Cleanup(func() {
		if isProcessAlive(oldPid) {
			killGroup(t, oldPid)
		}
	})
	if err := manager.registerStrictOverlapRequest("tunnel", "identity-change", oldPid, time.Second); err != nil {
		t.Fatalf("register request: %v", err)
	}
	manager.mutateProcess("tunnel", func(process *Process) {
		wrongStartTime := "wrong start time"
		process.StartTime = &wrongStartTime
	})

	if _, _, claimed := manager.claimStrictOverlapRequest("tunnel"); claimed {
		t.Fatal("request claimed after original runtime identity changed")
	}
	_, err, done := manager.settleStrictOverlapRequest("tunnel", "identity-change", false)
	if !done || err == nil {
		t.Fatalf("changed identity outcome = (%v,%v), want visible failure", err, done)
	}
}

func TestStrictOverlapCandidateCannotFinalizeAfterDeadline(t *testing.T) {
	manager, _, candidatePid, requestId, startTime, logPath := prepareClaimedStrictOverlapCandidate(t)
	manager.mutateProcess("tunnel", func(process *Process) {
		expired := nowUnix() - 1
		process.StrictOverlapDeadline = &expired
	})

	if manager.finalizeStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime, logPath) {
		t.Fatal("candidate finalized after its request deadline")
	}
	if err := manager.cleanupStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime); err != nil {
		t.Fatalf("clean expired candidate: %v", err)
	}
}

func TestStrictOverlapCandidateCannotFinalizeAfterOriginalChanges(t *testing.T) {
	manager, _, candidatePid, requestId, startTime, logPath := prepareClaimedStrictOverlapCandidate(t)
	manager.mutateProcess("tunnel", func(process *Process) {
		wrongStartTime := "wrong start time"
		process.StartTime = &wrongStartTime
	})

	if manager.finalizeStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime, logPath) {
		t.Fatal("candidate finalized after original runtime identity changed")
	}
	if err := manager.cleanupStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime); err != nil {
		t.Fatalf("clean rejected candidate: %v", err)
	}
}

func TestStrictOverlapCleanupFailurePreservesCandidateOwnership(t *testing.T) {
	manager, _, candidatePid, requestId, startTime, _ := prepareClaimedStrictOverlapCandidate(t)
	if err := manager.cleanupStrictOverlapCandidate("tunnel", requestId, candidatePid, "wrong start time"); err == nil {
		t.Fatal("cleanup accepted a candidate with mismatched identity")
	}
	definition, _ := manager.definition("tunnel")
	if definition.StrictOverlapCandidatePid == nil || *definition.StrictOverlapCandidatePid != candidatePid || definition.StrictOverlapError == "" {
		t.Fatalf("cleanup failure erased candidate ownership: %+v", definition)
	}
	if err := manager.cleanupStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime); err != nil {
		t.Fatalf("clean candidate using verified identity: %v", err)
	}
}

func TestRestartProcessStrictOverlapDelegatesAndServesWithoutRefusal(t *testing.T) {
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	cleanupManagedProcess(t, manager, "tunnel")
	setHeartbeat(t, manager, os.Getpid()+654321, 0)
	// A separate watcher must see a CLI request even after caching its absence.
	watcher := New(manager.Root())
	if watcher.fulfillStrictOverlapSpawnRequest("tunnel") {
		t.Fatal("watcher spawned without a request")
	}

	type restartResult struct {
		pid int
		err error
	}
	done := make(chan restartResult, 1)
	go func() {
		pid, err := manager.RestartProcessStrictOverlap("tunnel")
		done <- restartResult{pid: pid, err: err}
	}()
	waitForStrictOverlapRequest(t, manager, "tunnel")
	if pid, alive := manager.Status("tunnel"); !alive || pid != oldPid {
		t.Fatalf("CLI spawned before watcher fulfillment: status=(%d,%v)", pid, alive)
	}

	probes := make(chan connectionProbeResult, 1)
	stopProbing := make(chan struct{})
	go probeUntilStopped(port, stopProbing, probes)
	if !watcher.fulfillStrictOverlapSpawnRequest("tunnel") {
		t.Fatal("watcher did not observe and fulfill external strict overlap request")
	}
	result := <-done
	close(stopProbing)
	probeResult := <-probes
	if probeResult.refusals != 0 || len(probeResult.otherErrors) != 0 {
		t.Fatalf("strict overlap continuity probes: refused=%d other_errors=%v", probeResult.refusals, probeResult.otherErrors)
	}
	if result.err != nil {
		t.Fatalf("strict overlap restart: %v", result.err)
	}
	if result.pid == oldPid || isProcessAlive(oldPid) {
		t.Fatalf("strict overlap did not retire old pid %d; replacement=%d", oldPid, result.pid)
	}
	waitForServing(t, port)
}

func waitForStrictOverlapRequest(t *testing.T, manager *Manager, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		definition, ok := manager.definition(name)
		if ok && definition.StrictOverlapRequestId != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("strict overlap request was not registered")
}

func prepareClaimedStrictOverlapCandidate(t *testing.T) (*Manager, int, int, string, string, string) {
	t.Helper()
	manager := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, manager, "tunnel", port, true)
	cleanupManagedProcess(t, manager, "tunnel")
	t.Cleanup(func() {
		if isProcessAlive(oldPid) {
			killGroup(t, oldPid)
		}
	})
	requestId := "strict-overlap-test-request"
	if err := manager.registerStrictOverlapRequest("tunnel", requestId, oldPid, 5*time.Second); err != nil {
		t.Fatalf("register request: %v", err)
	}
	definition, _, claimed := manager.claimStrictOverlapRequest("tunnel")
	if !claimed {
		t.Fatal("request was not claimed")
	}
	candidatePid, logPath, err := manager.spawnWithRetryOverlap("tunnel", definition.Command, definition.Workdir, definition.Port)
	if err != nil {
		t.Fatalf("spawn candidate: %v", err)
	}
	startTime := processStartTime(candidatePid)
	if !manager.recordStrictOverlapCandidate("tunnel", requestId, candidatePid, startTime) {
		t.Fatal("candidate ownership was not recorded")
	}
	return manager, oldPid, candidatePid, requestId, startTime, logPath
}

func waitForStrictOverlapClaim(t *testing.T, manager *Manager, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		definition, ok := manager.definition(name)
		if ok && definition.StrictOverlapClaimed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("strict overlap request was not claimed")
}

type connectionProbeResult struct {
	refusals    int
	otherErrors []error
}

func probeUntilStopped(port int, stop <-chan struct{}, result chan<- connectionProbeResult) {
	probes := connectionProbeResult{}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			result <- probes
			return
		case <-ticker.C:
		}
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				probes.refusals++
			} else {
				probes.otherErrors = append(probes.otherErrors, err)
			}
			continue
		}
		if err := connection.Close(); err != nil {
			probes.otherErrors = append(probes.otherErrors, fmt.Errorf("close probe connection: %w", err))
		}
	}
}
