package manager

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// isolatePrefix namespaces every isolate-mode LaunchAgent label, keeping it in
// the same "com.darrenoakey.*" family as auto's own daemon and every
// hand-written per-app LaunchAgent it replaces.
const isolatePrefix = "com.darrenoakey."

// isolatePollInterval/isolatePollAttempts bound how long StartProcess/
// RestartProcess wait for launchd to report a pid after bootstrap/kickstart,
// mirroring SpawnVerifyDelay's role for direct spawns.
const (
	isolatePollInterval = 100 * time.Millisecond
	isolatePollAttempts = 50 // 5s total
)

// SetIsolate configures (or clears) isolate mode for a process: whether
// StartProcess/StopProcess/RestartProcess/processStatus delegate to a
// dedicated launchd LaunchAgent instead of auto's own daemon-managed
// fork/exec. Refuses to flip the mode while the process is alive under its
// CURRENT mode: doing so would silently orphan the running instance, since
// every subsequent StopProcess/processStatus call would immediately start
// querying the other backend instead. Callers must stop the process first.
func (m *Manager) SetIsolate(name string, isolate bool) error {
	def, ok := m.definition(name)
	if !ok {
		return fmt.Errorf("process %s not found", name)
	}
	if def.Isolate != isolate {
		if _, alive := m.processStatus(name); alive {
			return fmt.Errorf("process %s is running; stop it before changing isolate mode", name)
		}
	}
	m.mutateProcess(name, func(p *Process) { p.Isolate = isolate })
	return nil
}

// isolatePidPattern extracts the pid from `launchctl list <label>` output,
// identical in shape to pidLinePattern in shutdown.go (kept separate since
// that one is scoped to auto's own fixed label).
var isolatePidPattern = regexp.MustCompile(`"PID"\s*=\s*(\d+)`)

// isolateNamePattern restricts characters allowed in a launchd label segment.
var isolateNamePattern = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// isolateLabel returns the launchd label for an isolate-mode process.
func isolateLabel(name string) string {
	return isolatePrefix + isolateNamePattern.ReplaceAllString(name, "-")
}

// isolateHomeDir returns the invoking user's home directory, falling back to
// the canonical single-user path this project always runs under.
func isolateHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return "/Users/darrenoakey"
}

// isolatePlistPath returns the LaunchAgent plist path for an isolate-mode
// process.
func isolatePlistPath(name string) string {
	return filepath.Join(isolateHomeDir(), "Library", "LaunchAgents", isolateLabel(name)+".plist")
}

// isolateGUITarget returns the launchctl gui/<uid> domain target for a label.
func isolateGUITarget(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

// isolateLogPath returns (creating the directory) the single log file an
// isolate-mode process's stdout/stderr are redirected to. Unlike the daemon's
// own daily-rotated logs, this is a flat file — launchd, not auto, owns the
// process, so auto's log-archive pass never touches it.
func (m *Manager) isolateLogPath(name string) string {
	dir := filepath.Join(m.root, "output", "logs", name)
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, name+".log")
}

// shimPath resolves the auto-shim binary installed alongside the auto CLI
// wrapper (see pkg/install). It is what actually execs interpreter-based
// commands without hitting the fresh-LaunchAgent Python init stall — see
// cmd/auto-shim/main.go.
func shimPath() (string, error) {
	p := filepath.Join(isolateHomeDir(), "bin", "auto-shim")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("auto-shim not installed at %s (run auto's ./run install)", p)
	}
	return p, nil
}

// isolatePlistTemplate mirrors pkg/install's daemon template. Substitution
// order: label, shim path, command, workdir(-or-empty), stdout, stderr, PATH.
const isolatePlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>%s</string>
    </array>
%s    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>LimitLoadToSessionType</key><string>Aqua</string>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key><string>%s</string>
        <key>LANG</key><string>en_AU.UTF-8</string>
    </dict>
</dict>
</plist>
`

// isolateDaemonPATH matches pkg/install's daemonPATH so isolate-mode
// processes see the same PATH as everything else auto runs.
const isolateDaemonPATH = "/Users/darrenoakey/.local/bin:/Users/darrenoakey/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// isolatePlistContent renders the LaunchAgent plist for an isolate-mode
// process.
func isolatePlistContent(label, shim, command, workdir, logPath string) string {
	workdirBlock := ""
	if workdir != "" {
		workdirBlock = fmt.Sprintf("    <key>WorkingDirectory</key><string>%s</string>\n", workdir)
	}
	return fmt.Sprintf(isolatePlistTemplate, label, shim, command, workdirBlock, logPath, logPath, isolateDaemonPATH)
}

// isolateWritePlist renders, writes, and lints the LaunchAgent plist for name.
func (m *Manager) isolateWritePlist(name, command, workdir string) (string, error) {
	shim, err := shimPath()
	if err != nil {
		return "", err
	}
	plist := isolatePlistPath(name)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return "", err
	}
	content := isolatePlistContent(isolateLabel(name), shim, command, workdir, m.isolateLogPath(name))
	if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("plutil", "-lint", plist).CombinedOutput(); err != nil {
		return "", fmt.Errorf("invalid isolate plist for %s: %s", name, out)
	}
	return plist, nil
}

// isolateStatus queries launchd directly for the live pid of an isolate-mode
// process — unlike processStatus for daemon-managed processes, there is no
// locally-recorded pid to trust: launchd, not auto's state file, owns the
// truth here.
func isolateStatus(name string) (int, bool) {
	out, err := exec.Command("launchctl", "list", isolateLabel(name)).Output()
	if err != nil {
		return 0, false
	}
	match := isolatePidPattern.FindSubmatch(out)
	if match == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// isolateBootstrapped reports whether the LaunchAgent job is registered with
// launchd at all (running or not) — `launchctl list` finds a registered-but-
// not-yet-running job too, so isolateStatus alone cannot distinguish "not
// bootstrapped" from "bootstrapped, briefly between restarts".
func isolateBootstrapped(name string) bool {
	return exec.Command("launchctl", "list", isolateLabel(name)).Run() == nil
}

// isolateStart writes/refreshes the LaunchAgent plist, bootstraps it if
// launchd does not already know about it, kickstarts it if it does but is not
// currently running, and polls for a pid — mirroring the direct-spawn path's
// SpawnVerifyDelay-bounded wait so callers see a converged start.
func (m *Manager) isolateStart(name, command, workdir string) (int, error) {
	if pid, alive := isolateStatus(name); alive {
		return 0, fmt.Errorf("process %s is already running with pid %d", name, pid)
	}
	if err := m.isolateLaunch(name, command, workdir); err != nil {
		return 0, err
	}
	return isolateWaitAlive(name)
}

// isolateRestart writes/refreshes the plist and kickstarts (or bootstraps)
// the job regardless of whether it is currently running — unlike
// isolateStart, an already-running process is not an error, matching
// RestartProcess's stop-then-start semantics for daemon-managed processes.
func (m *Manager) isolateRestart(name, command, workdir string) (int, error) {
	if err := m.isolateLaunch(name, command, workdir); err != nil {
		return 0, err
	}
	return isolateWaitAlive(name)
}

// isolateLaunch writes/refreshes the plist, then bootstraps the job if
// launchd does not already know about it, or kickstarts (restarts) it if it
// does.
func (m *Manager) isolateLaunch(name, command, workdir string) error {
	plist, err := m.isolateWritePlist(name, command, workdir)
	if err != nil {
		return err
	}
	target := isolateGUITarget(isolateLabel(name))
	if isolateBootstrapped(name) {
		if out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl kickstart %s: %v: %s", name, err, out)
		}
		return nil
	}
	if out, err := exec.Command("launchctl", "bootstrap", isolateGUIDomain(), plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %v: %s", name, err, out)
	}
	return nil
}

// isolateWaitAlive polls launchd for a pid, mirroring the direct-spawn
// path's SpawnVerifyDelay-bounded wait so callers see a converged start.
func isolateWaitAlive(name string) (int, error) {
	for range isolatePollAttempts {
		if pid, alive := isolateStatus(name); alive {
			return pid, nil
		}
		time.Sleep(isolatePollInterval)
	}
	return 0, fmt.Errorf("launchd did not report %s running within %s", name, isolatePollAttempts*isolatePollInterval)
}

// isolateGUIDomain returns the launchctl gui/<uid> domain for the current
func isolateGUIDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// isolateStop tears down the LaunchAgent job. bootout sends SIGTERM to the
// job (routing through the shimmed command's own signal handling) and
// deregisters it from launchd; a subsequent isolateStart re-bootstraps it
// cleanly. Unlike terminateProcessGroup's SIGTERM/SIGKILL escalation, this
// relies on launchd's own teardown — it already waits for the job to exit
// before `bootout` returns.
func (m *Manager) isolateStop(name string) error {
	if !isolateBootstrapped(name) {
		return nil
	}
	target := isolateGUITarget(isolateLabel(name))
	if out, err := exec.Command("launchctl", "bootout", target).CombinedOutput(); err != nil {
		// "no such process" (already gone) is not a failure.
		if !strings.Contains(string(out), "Could not find") && !strings.Contains(string(out), "No such process") {
			return fmt.Errorf("launchctl bootout %s: %v: %s", name, err, out)
		}
	}
	return nil
}

// isolateRemoveArtifacts boots the job out (if loaded) and deletes its plist,
// leaving no trace in ~/Library/LaunchAgents. Best-effort: a missing plist is
// not an error.
func (m *Manager) isolateRemoveArtifacts(name string) error {
	if err := m.isolateStop(name); err != nil {
		return err
	}
	plist := isolatePlistPath(name)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", plist, err)
	}
	return nil
}
