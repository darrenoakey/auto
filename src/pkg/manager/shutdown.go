package manager

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// LaunchAgentLabel is the launchd label for the auto watch daemon.
const LaunchAgentLabel = "com.darrenoakey.auto"

// pidLinePattern extracts the pid from `launchctl list <label>` output.
var pidLinePattern = regexp.MustCompile(`"PID"\s*=\s*(\d+)`)

// killTarget is a running managed process targeted for shutdown.
type killTarget struct {
	name string
	pid  int
	pgid int
}

// ShutdownAll stops all running managed processes in parallel without marking
// them explicitly stopped, so they recover after the next boot.
func (m *Manager) ShutdownAll() {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	isolatesDone := m.shutdownIsolates()
	defer func() { <-isolatesDone }()
	m.shutdownTargets(m.runningTargets())
}

// StopAllForRespawn stops only watcher-owned groups. Isolated jobs already have
// their own launchd identity and must stay registered: the watcher deliberately
// never supervises them. Unlike ShutdownAll, this is not a shutdown operation.
func (m *Manager) StopAllForRespawn() {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.shutdownTargets(m.runningTargets())
}

func (m *Manager) shutdownTargets(targets []killTarget) {
	if len(targets) == 0 {
		return
	}
	for _, t := range targets {
		_ = syscall.Kill(-t.pgid, syscall.SIGTERM)
	}
	alive := waitForTargets(targets, SigtermTimeout)
	if len(alive) == 0 {
		return
	}
	for _, t := range alive {
		_ = syscall.Kill(-t.pgid, syscall.SIGKILL)
	}
	for _, t := range waitForTargets(alive, 2*time.Second) {
		fmt.Printf("Warning: process %s (pid %d) survived SIGKILL\n", t.name, t.pid)
	}
}

// shutdownIsolates unloads even registered-but-dead jobs, not just live PIDs.
// Sending their process groups SIGTERM would let launchd KeepAlive respawn them.
// Plists and explicitly-stopped flags remain unchanged for the next login.
func (m *Manager) shutdownIsolates() <-chan struct{} {
	done := make(chan struct{})
	var workers sync.WaitGroup
	for _, name := range m.definedNames() {
		if def, ok := m.definition(name); ok && def.Isolate {
			workers.Go(func() {
				operationContext, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				if err := m.isolateStopContext(operationContext, name); err != nil {
					fmt.Printf("Warning: failed to unload isolated service %s: %v\n", name, err)
				}
			})
		}
	}
	go func() { workers.Wait(); close(done) }()
	return done
}

// runningTargets collects all currently-running directly managed processes.
func (m *Manager) runningTargets() []killTarget {
	var targets []killTarget
	for _, name := range sortedNames(m.loadStateFile().Processes) {
		if def, ok := m.definition(name); !ok || def.Isolate {
			continue
		}
		pid, alive := m.processStatus(name)
		if !alive {
			continue
		}
		if pgid, err := syscall.Getpgid(pid); err == nil {
			targets = append(targets, killTarget{name: name, pid: pid, pgid: pgid})
		}
	}
	return targets
}

// waitForTargets polls until all targets die or the timeout elapses, returning
// any survivors.
func waitForTargets(targets []killTarget, timeout time.Duration) []killTarget {
	deadline := time.Now().Add(timeout)
	alive := targets
	for len(alive) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		remaining := alive[:0:0]
		for _, t := range alive {
			if processGroupAlive(t.pgid) {
				remaining = append(remaining, t)
			}
		}
		alive = remaining
	}
	return alive
}

// AutoDaemonPid returns the pid of the running auto watch daemon via launchctl,
// or 0 if it is not running.
func AutoDaemonPid() int {
	out, err := exec.Command("launchctl", "list", LaunchAgentLabel).Output()
	if err != nil {
		return 0
	}
	match := pidLinePattern.FindSubmatch(out)
	if match == nil {
		return 0
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0
	}
	return pid
}

// LaunchAgentPath returns the installed plist path for the watch daemon.
func LaunchAgentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/Users/darrenoakey"
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

// ShutdownForReboot kills the watch daemon first (so it cannot restart anything),
// then obliterates all managed processes. Processes are not marked explicitly
// stopped, so they restart after the next boot.
func (m *Manager) ShutdownForReboot() {
	m.InhibitStarts()
	daemonPid := AutoDaemonPid()
	plist := LaunchAgentPath()
	if _, err := os.Stat(plist); err == nil {
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), plist).Run()
	}
	if daemonPid != 0 {
		waitForProcessDeath(daemonPid, SigtermTimeout)
	}
	m.ShutdownAll()
}
