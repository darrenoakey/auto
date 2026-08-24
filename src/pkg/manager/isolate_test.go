package manager

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestIsolateLabelSanitizesName confirms the launchd label stays in auto's own
// label family and strips characters launchd label segments cannot contain.
func TestIsolateLabelSanitizesName(t *testing.T) {
	if got := isolateLabel("calendar-display"); got != "com.darrenoakey.calendar-display" {
		t.Fatalf("isolateLabel = %q", got)
	}
	if got := isolateLabel("weird name!"); got != "com.darrenoakey.weird-name-" {
		t.Fatalf("isolateLabel did not sanitize: %q", got)
	}
}

// TestIsolateGUITarget confirms the gui/<uid>/<label> target format launchctl
// bootstrap/kickstart/bootout require.
func TestIsolateGUITarget(t *testing.T) {
	got := isolateGUITarget("com.darrenoakey.svc")
	want := fmt.Sprintf("gui/%d/com.darrenoakey.svc", os.Getuid())
	if got != want {
		t.Fatalf("isolateGUITarget = %q, want %q", got, want)
	}
}

// TestIsolatePlistContentIncludesWorkdirWhenSet confirms the rendered plist
// carries a WorkingDirectory key only when a workdir was supplied, and always
// carries the shim (not the raw interpreter command) as ProgramArguments[0]
// so the process is exec'd directly rather than hung under a fresh
// LaunchAgent's Python init.
func TestIsolatePlistContentIncludesWorkdirWhenSet(t *testing.T) {
	content := isolatePlistContent("com.darrenoakey.svc", "/Users/darrenoakey/bin/auto-shim", "python3 run.py", "/Users/darrenoakey/src/svc", "/tmp/svc.log")
	if !strings.Contains(content, "<key>WorkingDirectory</key><string>/Users/darrenoakey/src/svc</string>") {
		t.Fatalf("plist missing WorkingDirectory: %s", content)
	}
	if !strings.Contains(content, "<string>/Users/darrenoakey/bin/auto-shim</string>") {
		t.Fatalf("plist should exec the shim, not the raw command: %s", content)
	}
	if !strings.Contains(content, "<string>python3 run.py</string>") {
		t.Fatalf("plist should still carry the raw command as the shim's argument: %s", content)
	}
	if !strings.Contains(content, "<key>LimitLoadToSessionType</key><string>Aqua</string>") {
		t.Fatalf("plist must run in the GUI session, not headless: %s", content)
	}
}

// TestIsolatePlistContentOmitsWorkdirWhenEmpty confirms an empty workdir
// leaves WorkingDirectory out entirely rather than emitting an empty key.
func TestIsolatePlistContentOmitsWorkdirWhenEmpty(t *testing.T) {
	content := isolatePlistContent("com.darrenoakey.svc", "/Users/darrenoakey/bin/auto-shim", "sleep 300", "", "/tmp/svc.log")
	if strings.Contains(content, "WorkingDirectory") {
		t.Fatalf("plist should omit WorkingDirectory when workdir is empty: %s", content)
	}
}

// TestSetIsolateRoundTrip confirms the flag persists and surfaces through
// ListProcesses for `ps` to render.
func TestSetIsolateRoundTrip(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	if err := m.SetIsolate("svc", true); err != nil {
		t.Fatalf("SetIsolate: %v", err)
	}
	infos := m.ListProcesses()
	if len(infos) != 1 || !infos[0].Isolate {
		t.Fatalf("ListProcesses did not report isolate: %+v", infos)
	}
}

// TestSetIsolateMissingProcessFails confirms it validates the process exists,
// matching SetRestartInterval's behavior.
func TestSetIsolateMissingProcessFails(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetIsolate("ghost", true); err == nil {
		t.Fatal("SetIsolate on an undefined process should fail")
	}
}

// TestSetIsolateRefusesWhileRunning confirms flipping mode on a live,
// daemon-managed process is rejected rather than silently orphaning the
// running instance (StopProcess would otherwise immediately start querying
// the wrong backend for it).
func TestSetIsolateRefusesWhileRunning(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	pid, err := m.StartProcess("svc")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	if err := m.SetIsolate("svc", true); err == nil {
		t.Fatal("SetIsolate should refuse to flip mode on a running process")
	}
	if got, alive := m.Status("svc"); !alive || got != pid {
		t.Fatalf("running process should be unaffected by the refused flip, got (%d,%v)", got, alive)
	}
}

// TestSetIsolateAllowedWhileStopped confirms the same flip succeeds once the
// process is not running.
func TestSetIsolateAllowedWhileStopped(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	if err := m.SetIsolate("svc", true); err != nil {
		t.Fatalf("SetIsolate on a stopped process should succeed: %v", err)
	}
}

// TestProcessStatusDelegatesToIsolate confirms an isolate-mode process's
// liveness is queried from launchd (via isolateStatus), not from auto's own
// recorded pid/start-time — which stays unset since auto never spawns it.
// The process is never bootstrapped here, so this stays a real, side-effect-
// free launchctl query (an unregistered label simply reports not-found).
func TestProcessStatusDelegatesToIsolate(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	if err := m.SetIsolate("svc", true); err != nil {
		t.Fatalf("SetIsolate: %v", err)
	}
	if pid, alive := m.Status("svc"); alive {
		t.Fatalf("unbootstrapped isolate process should not be alive, got pid %d", pid)
	}
}

// TestWatchTickSkipsIsolateProcesses confirms the daemon's own tick never
// attempts to supervise (and, critically, never spawns) an isolate-mode
// process — that is launchd's job exclusively. Regressing this would make
// WatchTick call StartProcess on a "dead" isolate definition, which
// bootstraps a real LaunchAgent job as a side effect of running `go test`.
func TestWatchTickSkipsIsolateProcesses(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	if err := m.SetIsolate("svc", true); err != nil {
		t.Fatalf("SetIsolate: %v", err)
	}
	m.WatchTick()
	t.Cleanup(func() { _ = m.isolateRemoveArtifacts("svc") })
	if isolateBootstrapped("svc") {
		t.Fatal("WatchTick must never bootstrap an isolate-mode process itself")
	}
	if _, alive := m.Status("svc"); alive {
		t.Fatal("WatchTick must not have started the isolate process")
	}
}

// TestRemoveProcessIsolateArtifacts confirms removing an isolate-mode
// process routes through isolateRemoveArtifacts (bootout + plist delete)
// rather than the daemon-managed StopProcess path, and succeeds cleanly when
// nothing was ever bootstrapped.
func TestRemoveProcessIsolateArtifacts(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	if err := m.SetIsolate("svc", true); err != nil {
		t.Fatalf("SetIsolate: %v", err)
	}
	if err := m.RemoveProcess("svc"); err != nil {
		t.Fatalf("RemoveProcess: %v", err)
	}
	if _, ok := m.definition("svc"); ok {
		t.Fatal("svc should be removed from config")
	}
}
