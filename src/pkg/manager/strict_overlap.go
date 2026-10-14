package manager

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"
	"time"
)

// RestartProcessStrictOverlap replaces a live process without ever falling
// back to a serial restart. Any replacement failure leaves the old process
// recorded and running; callers must use start separately when it is dead.
func (m *Manager) RestartProcessStrictOverlap(name string) (int, error) {
	definition, ok := m.definition(name)
	if !ok {
		return 0, fmt.Errorf("process %s not found in config", name)
	}
	if definition.Isolate {
		return 0, fmt.Errorf("strict overlap restart is not supported for launchd-isolated process %s", name)
	}
	oldPid, alive := m.processStatus(name)
	if !alive {
		return 0, fmt.Errorf("strict overlap restart requires live process %s; use start separately", name)
	}
	newPid, err := m.requestStrictOverlap(name, oldPid, OverlapSpawnRequestTTL)
	if err != nil {
		return 0, err
	}
	if err := m.terminateValidatedProcess(name, oldPid, definition.StartTime); err != nil {
		if rollbackError := m.rollbackStrictReplacement(name, definition, newPid); rollbackError != nil {
			return 0, fmt.Errorf("%w; strict overlap rollback failed: %v", err, rollbackError)
		}
		return 0, err
	}
	m.resetRestartAttempt(name)
	return newPid, nil
}

func (m *Manager) requestStrictOverlap(name string, oldPid int, timeout time.Duration) (int, error) {
	daemonPid := m.liveDaemon()
	if daemonPid == 0 {
		return 0, fmt.Errorf("strict overlap restart requires a live watch daemon for %s", name)
	}
	requestId, err := newStrictOverlapRequestId()
	if err != nil {
		return 0, fmt.Errorf("create strict overlap request for %s: %w", name, err)
	}
	if err := m.registerStrictOverlapRequest(name, requestId, oldPid, timeout); err != nil {
		return 0, err
	}
	if daemonPid == os.Getpid() {
		m.fulfillStrictOverlapSpawnRequest(name)
	}
	return m.awaitStrictOverlap(name, requestId, oldPid, timeout)
}

func newStrictOverlapRequestId() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (m *Manager) registerStrictOverlapRequest(name, requestId string, oldPid int, timeout time.Duration) error {
	registered := false
	reason := "already has a strict overlap restart in flight"
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok {
			return false
		}
		if process.StrictOverlapRequestId != "" {
			if strictOverlapRequestPending(process) {
				return false
			}
			clearStrictOverlapFields(process)
		}
		if !strictOverlapOriginalMatches(process, oldPid, process.StartTime) {
			reason = "runtime identity changed before strict overlap registration"
			return false
		}
		initializeStrictOverlapRequest(process, requestId, oldPid, timeout)
		registered = true
		return true
	})
	if !registered {
		return fmt.Errorf("process %s %s", name, reason)
	}
	return nil
}

func initializeStrictOverlapRequest(process *Process, requestId string, oldPid int, timeout time.Duration) {
	now := nowUnix()
	deadline := now + timeout.Seconds()
	originalStartTime := *process.StartTime
	process.StrictOverlapRequestId = requestId
	process.StrictOverlapRequestedAt = &now
	process.StrictOverlapDeadline = &deadline
	process.StrictOverlapOriginalPid = &oldPid
	process.StrictOverlapOriginalStartTime = &originalStartTime
	process.StrictOverlapClaimed = false
	process.StrictOverlapCancelled = false
	process.StrictOverlapCandidatePid = nil
	process.StrictOverlapCandidateStartTime = nil
	process.StrictOverlapResultPid = nil
	process.StrictOverlapError = ""
}

func strictOverlapRequestLive(process *Process) bool {
	if process == nil || process.StrictOverlapRequestId == "" || process.StrictOverlapRequestedAt == nil {
		return false
	}
	if process.StrictOverlapDeadline != nil {
		return nowUnix() < *process.StrictOverlapDeadline
	}
	return nowUnix()-*process.StrictOverlapRequestedAt < OverlapSpawnRequestTTL.Seconds()
}

func strictOverlapRequestPending(process *Process) bool {
	return strictOverlapRequestLive(process) || process.StrictOverlapClaimed ||
		process.StrictOverlapCandidatePid != nil || process.StrictOverlapResultPid != nil || process.StrictOverlapError != ""
}

func (m *Manager) awaitStrictOverlap(name, requestId string, oldPid int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid, err, done := m.settleStrictOverlapRequest(name, requestId, false); done {
			return pid, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid, err, done := m.settleStrictOverlapRequest(name, requestId, true); done {
		return pid, err
	}
	return 0, fmt.Errorf("watch daemon did not strict-overlap-spawn %s within %s; old pid %d remains active", name, timeout, oldPid)
}

func (m *Manager) settleStrictOverlapRequest(name, requestId string, cancel bool) (int, error, bool) {
	var pid int
	var outcomeError error
	done := false
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || process.StrictOverlapRequestId != requestId {
			outcomeError = fmt.Errorf("strict overlap request for %s disappeared", name)
			done = true
			return false
		}
		return settleStrictOverlapProcess(process, name, cancel, &pid, &outcomeError, &done)
	})
	return pid, outcomeError, done
}

func settleStrictOverlapProcess(process *Process, name string, cancel bool, pid *int, outcomeError *error, done *bool) bool {
	if process.StrictOverlapResultPid != nil {
		*pid = *process.StrictOverlapResultPid
		*done = true
		clearStrictOverlapFields(process)
		return true
	}
	if process.StrictOverlapError != "" {
		*outcomeError = fmt.Errorf("strict overlap replacement for %s failed: %s", name, process.StrictOverlapError)
		*done = true
		if process.StrictOverlapCandidatePid == nil {
			clearStrictOverlapFields(process)
			return true
		}
		return false
	}
	if !cancel {
		return false
	}
	if process.StrictOverlapClaimed || process.StrictOverlapCandidatePid != nil {
		process.StrictOverlapCancelled = true
		return true
	}
	clearStrictOverlapFields(process)
	return true
}

func (m *Manager) clearStrictOverlapRequest(name, requestId string) {
	m.mutateProcess(name, func(process *Process) {
		if process.StrictOverlapRequestId == requestId {
			clearStrictOverlapFields(process)
		}
	})
}

func clearStrictOverlapFields(process *Process) {
	process.StrictOverlapRequestId = ""
	process.StrictOverlapRequestedAt = nil
	process.StrictOverlapDeadline = nil
	process.StrictOverlapOriginalPid = nil
	process.StrictOverlapOriginalStartTime = nil
	process.StrictOverlapClaimed = false
	process.StrictOverlapCancelled = false
	process.StrictOverlapCandidatePid = nil
	process.StrictOverlapCandidateStartTime = nil
	process.StrictOverlapResultPid = nil
	process.StrictOverlapError = ""
}

func (m *Manager) claimStrictOverlapRequest(name string) (*Process, string, bool) {
	var definition *Process
	var requestId string
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || !strictOverlapRequestClaimable(process) {
			return false
		}
		if !strictOverlapStoredOriginalMatches(process) {
			process.StrictOverlapError = "original runtime identity changed before request claim"
			return true
		}
		process.StrictOverlapClaimed = true
		requestId = process.StrictOverlapRequestId
		copy := *process
		definition = &copy
		return true
	})
	return definition, requestId, definition != nil
}

func strictOverlapRequestClaimable(process *Process) bool {
	return strictOverlapRequestLive(process) && !process.StrictOverlapClaimed && !process.StrictOverlapCancelled &&
		process.StrictOverlapCandidatePid == nil && process.StrictOverlapResultPid == nil && process.StrictOverlapError == ""
}

// fulfillStrictOverlapSpawnRequest handles one watcher-owned strict request.
func (m *Manager) fulfillStrictOverlapSpawnRequest(name string) bool {
	// The common case has no request. Avoid locked whole-registry reads and
	// cleanup writes for every service on every tick. definition observes
	// external CLI writes; claimStrictOverlapRequest still rechecks under lock.
	if process, ok := m.definition(name); !ok || process.StrictOverlapRequestId == "" {
		return false
	}
	definition, requestId, claimed := m.claimStrictOverlapRequest(name)
	if !claimed {
		m.retryStrictOverlapCleanup(name)
		m.discardExpiredStrictOverlapRequest(name)
		return false
	}
	pid, logPath, err := m.spawnWithRetryOverlap(name, definition.Command, definition.Workdir, definition.Port)
	if err != nil {
		m.finishStrictOverlapFailure(name, requestId, err)
		return false
	}
	return m.completeStrictOverlapCandidate(name, requestId, pid, logPath)
}

func (m *Manager) completeStrictOverlapCandidate(name, requestId string, pid int, logPath string) bool {
	startTime := processStartTime(pid)
	if !m.recordStrictOverlapCandidate(name, requestId, pid, startTime) {
		if err := m.cleanupStrictOverlapCandidate(name, requestId, pid, startTime); err != nil {
			fmt.Printf("Failed to clean unrecorded strict-overlap candidate %s pid %d: %v\n", name, pid, err)
		}
		return false
	}
	if startTime != "" && m.finalizeStrictOverlapCandidate(name, requestId, pid, startTime, logPath) {
		reapWhenDone(pid)
		return true
	}
	if err := m.cleanupStrictOverlapCandidate(name, requestId, pid, startTime); err != nil {
		fmt.Printf("Failed to clean strict-overlap candidate %s pid %d: %v\n", name, pid, err)
	}
	return false
}

func (m *Manager) recordStrictOverlapCandidate(name, requestId string, pid int, startTime string) bool {
	recorded := false
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || process.StrictOverlapRequestId != requestId || !process.StrictOverlapClaimed {
			return false
		}
		process.StrictOverlapCandidatePid = &pid
		process.StrictOverlapCandidateStartTime = &startTime
		recorded = true
		return true
	})
	return recorded
}

func (m *Manager) finalizeStrictOverlapCandidate(name, requestId string, pid int, startTime, logPath string) bool {
	finalized := false
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || !strictOverlapCandidateFinalizable(process, requestId, pid, startTime) {
			return false
		}
		process.Pid = &pid
		process.StartTime = &startTime
		process.LogPath = logPath
		process.StartingSince = nil
		process.StrictOverlapClaimed = false
		process.StrictOverlapResultPid = &pid
		finalized = true
		return true
	})
	return finalized
}

func strictOverlapCandidateFinalizable(process *Process, requestId string, pid int, startTime string) bool {
	return process.StrictOverlapRequestId == requestId && !process.StrictOverlapCancelled &&
		strictOverlapRequestLive(process) && strictOverlapStoredOriginalMatches(process) &&
		process.StrictOverlapCandidatePid != nil && *process.StrictOverlapCandidatePid == pid &&
		process.StrictOverlapCandidateStartTime != nil && *process.StrictOverlapCandidateStartTime == startTime &&
		isOurProcess(pid, &startTime)
}

func (m *Manager) finishStrictOverlapFailure(name, requestId string, spawnError error) {
	m.mutateProcess(name, func(process *Process) {
		if process.StrictOverlapRequestId != requestId {
			return
		}
		if process.StrictOverlapCancelled {
			clearStrictOverlapFields(process)
			return
		}
		process.StrictOverlapClaimed = false
		process.StrictOverlapError = spawnError.Error()
	})
}

func (m *Manager) cleanupStrictOverlapCandidate(name, requestId string, pid int, startTime string) error {
	if isProcessAlive(pid) {
		if startTime == "" || !isOurProcess(pid, &startTime) {
			return m.persistStrictOverlapCleanupError(name, requestId, pid, "candidate runtime identity does not match")
		}
		processGroup, err := syscall.Getpgid(pid)
		if err != nil {
			return m.persistStrictOverlapCleanupError(name, requestId, pid, fmt.Sprintf("get process group: %v", err))
		}
		if err := m.terminateProcessGroup(name+" strict-overlap candidate", pid, processGroup); err != nil {
			return m.persistStrictOverlapCleanupError(name, requestId, pid, err.Error())
		}
	}
	m.clearStrictOverlapRequest(name, requestId)
	return nil
}

func (m *Manager) persistStrictOverlapCleanupError(name, requestId string, pid int, detail string) error {
	cleanupError := fmt.Errorf("cleanup candidate pid %d: %s", pid, detail)
	m.mutateProcess(name, func(process *Process) {
		if process.StrictOverlapRequestId == requestId {
			process.StrictOverlapClaimed = false
			process.StrictOverlapError = cleanupError.Error()
		}
	})
	return cleanupError
}

func (m *Manager) retryStrictOverlapCleanup(name string) {
	process, ok := m.definition(name)
	if !ok || process.StrictOverlapRequestId == "" || process.StrictOverlapCandidatePid == nil || process.StrictOverlapResultPid != nil {
		return
	}
	startTime := ""
	if process.StrictOverlapCandidateStartTime != nil {
		startTime = *process.StrictOverlapCandidateStartTime
	}
	if err := m.cleanupStrictOverlapCandidate(name, process.StrictOverlapRequestId, *process.StrictOverlapCandidatePid, startTime); err != nil {
		fmt.Printf("Failed to retry strict-overlap cleanup for %s: %v\n", name, err)
	}
}

func (m *Manager) discardExpiredStrictOverlapRequest(name string) {
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || process.StrictOverlapRequestId == "" || strictOverlapRequestPending(process) {
			return false
		}
		clearStrictOverlapFields(process)
		return true
	})
}

func strictOverlapStoredOriginalMatches(process *Process) bool {
	if process.StrictOverlapOriginalPid == nil || process.StrictOverlapOriginalStartTime == nil {
		return false
	}
	return strictOverlapOriginalMatches(process, *process.StrictOverlapOriginalPid, process.StrictOverlapOriginalStartTime)
}

func strictOverlapOriginalMatches(process *Process, pid int, startTime *string) bool {
	return process.Pid != nil && *process.Pid == pid && process.StartTime != nil && startTime != nil &&
		startTimesMatch(*process.StartTime, *startTime) && isOurProcess(pid, startTime)
}

func (m *Manager) terminateValidatedProcess(name string, pid int, startTime *string) error {
	if !isOurProcess(pid, startTime) {
		return fmt.Errorf("refusing to stop old process %s with pid %d: recorded identity no longer matches", name, pid)
	}
	processGroup, err := syscall.Getpgid(pid)
	if err != nil {
		return fmt.Errorf("get process group for old process %s with pid %d: %w", name, pid, err)
	}
	return m.terminateProcessGroup(name, pid, processGroup)
}

func (m *Manager) rollbackStrictReplacement(name string, original *Process, candidatePid int) error {
	current, ok := m.definition(name)
	if !ok || current.Pid == nil || *current.Pid != candidatePid || current.StartTime == nil {
		return fmt.Errorf("candidate runtime ownership changed before rollback")
	}
	if err := m.cleanupFinalizedCandidate(name, candidatePid, current.StartTime); err != nil {
		return err
	}
	if original.Pid == nil || !isOurProcess(*original.Pid, original.StartTime) {
		return fmt.Errorf("original runtime is no longer alive after candidate cleanup")
	}
	if !m.restoreStrictOverlapOriginal(name, original, candidatePid, current.StartTime) {
		return fmt.Errorf("candidate runtime ownership changed while restoring original")
	}
	return nil
}

func (m *Manager) cleanupFinalizedCandidate(name string, candidatePid int, startTime *string) error {
	if isOurProcess(candidatePid, startTime) {
		processGroup, err := syscall.Getpgid(candidatePid)
		if err != nil {
			return fmt.Errorf("get candidate process group: %w", err)
		}
		return m.terminateProcessGroup(name+" strict-overlap candidate", candidatePid, processGroup)
	}
	if isProcessAlive(candidatePid) {
		return fmt.Errorf("candidate pid %d identity changed before rollback", candidatePid)
	}
	return nil
}

func (m *Manager) restoreStrictOverlapOriginal(name string, original *Process, candidatePid int, candidateStartTime *string) bool {
	restored := false
	m.withState(func(data *stateFile) bool {
		process, ok := data.Processes[name]
		if !ok || process.Pid == nil || *process.Pid != candidatePid || process.StartTime == nil ||
			candidateStartTime == nil || !startTimesMatch(*process.StartTime, *candidateStartTime) {
			return false
		}
		process.Pid = original.Pid
		process.StartTime = original.StartTime
		process.LogPath = original.LogPath
		process.StartingSince = original.StartingSince
		process.ExplicitlyStopped = original.ExplicitlyStopped
		restored = true
		return true
	})
	return restored
}
