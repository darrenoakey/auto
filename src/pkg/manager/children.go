package manager

import (
	"fmt"
	"sort"
	"syscall"
)

// ownChild is a child this process spawned for a managed service. The watch
// daemon is the only long-lived spawner, so its registry names every service
// process it started. Entries are pruned lazily: a child that exited, or a
// recycled pid, no longer matches its recorded kernel start time.
type ownChild struct {
	name      string
	startTime string
	logPath   string
}

// registerChild records a freshly spawned service child.
func (m *Manager) registerChild(name string, pid int, logPath string) {
	startTime := processStartTime(pid)
	if startTime == "" {
		return
	}
	m.childrenMu.Lock()
	defer m.childrenMu.Unlock()
	if m.children == nil {
		m.children = map[int]ownChild{}
	}
	m.children[pid] = ownChild{name: name, startTime: startTime, logPath: logPath}
}

// strayOwnChildren returns, in pid order, the live children this process
// spawned for name other than recorded, pruning name's dead or recycled
// entries. Only name's own entries are inspected: the watch loop calls this
// for every dead service on every tick, so it must cost no process lookups
// for other services' children (see the idle-tick CPU guard in AGENTS.md).
func (m *Manager) strayOwnChildren(name string, recorded int) []int {
	m.childrenMu.Lock()
	defer m.childrenMu.Unlock()
	var strays []int
	for pid, child := range m.children {
		if child.name != name {
			continue
		}
		startTime := child.startTime
		if !isOurProcess(pid, &startTime) {
			delete(m.children, pid)
			continue
		}
		if pid != recorded {
			strays = append(strays, pid)
		}
	}
	sort.Ints(strays)
	return strays
}

func (m *Manager) ownChildRecord(pid int) (ownChild, bool) {
	m.childrenMu.Lock()
	defer m.childrenMu.Unlock()
	child, ok := m.children[pid]
	return child, ok
}

// recoverOwnChild repairs a service whose record lost track of a child this
// daemon spawned and that is still running. Without it the watch loop treats
// the service as dead and restarts it forever into the port its own live child
// still holds ("auto never kills unmanaged port holders"), while every
// supervisor-attested caller sees a listener owned by nobody: a host's
// agentd-gauge looped 189 times and its updater refused every release for a
// day. A running service's sole stray child is adopted; an explicitly stopped
// service's stray children are stopped, completing the operator's stop.
// Nothing is touched while a start, delegated spawn or overlap is in flight.
// Returns whether it acted, so the caller skips the normal dead restart.
func (m *Manager) recoverOwnChild(name string) bool {
	def, ok := m.definition(name)
	if !ok || def.Isolate || startClaimIsLive(def) || def.SpawnRequestedAt != nil ||
		def.OverlapSpawnRequestedAt != nil || def.StrictOverlapRequestId != "" {
		return false
	}
	recorded := 0
	if def.Pid != nil {
		recorded = *def.Pid
	}
	strays := m.strayOwnChildren(name, recorded)
	if len(strays) == 0 {
		return false
	}
	if def.ExplicitlyStopped {
		for _, pid := range strays {
			m.stopStrayChild(name, pid)
		}
		return true
	}
	if len(strays) != 1 {
		fmt.Printf("Not adopting %s: %d live orphaned children %v are ambiguous\n", name, len(strays), strays)
		return false
	}
	return m.adoptChild(name, strays[0])
}

// adoptChild records pid as name's process if the record still names no live
// process and the service is still not stopped, under the state lock.
func (m *Manager) adoptChild(name string, pid int) bool {
	child, ok := m.ownChildRecord(pid)
	if !ok {
		return false
	}
	adopted := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.ExplicitlyStopped || startClaimIsLive(p) {
			return false
		}
		if p.Pid != nil && isOurProcess(*p.Pid, p.StartTime) {
			return false
		}
		startTime := child.startTime
		p.Pid = &pid
		p.StartTime = &startTime
		p.LogPath = child.logPath
		p.RestartAttempt = 0
		p.LastRestartTime = nil
		adopted = true
		return true
	})
	if adopted {
		fmt.Printf("Adopted orphaned child of %s with pid %d\n", name, pid)
	}
	return adopted
}

// stopStrayChild terminates one of this daemon's own children whose service
// was explicitly stopped while the child was starting.
func (m *Manager) stopStrayChild(name string, pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return
	}
	if err := m.terminateProcessGroup(name, pid, pgid); err != nil {
		fmt.Printf("Failed to stop orphaned child of stopped %s: %v\n", name, err)
		return
	}
	fmt.Printf("Stopped orphaned child of stopped %s with pid %d\n", name, pid)
}
