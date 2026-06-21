package manager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	targets := m.runningTargets()
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

// runningTargets collects all currently-running managed processes with pgids.
func (m *Manager) runningTargets() []killTarget {
	var targets []killTarget
	for _, name := range sortedNames(m.loadStateFile().Processes) {
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
			if isProcessAlive(t.pid) {
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
