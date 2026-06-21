package manager

import (
	"path/filepath"
	"testing"
)

func TestShutdownAllKillsRunning(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "a", "sleep 300", nil)
	mustAdd(t, m, "b", "sleep 300", nil)
	pidA, err := m.StartProcess("a")
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	pidB, err := m.StartProcess("b")
	if err != nil {
		t.Fatalf("start b: %v", err)
	}
	m.ShutdownAll()
	if isProcessAlive(pidA) || isProcessAlive(pidB) {
		t.Fatalf("ShutdownAll left survivors: a=%v b=%v", isProcessAlive(pidA), isProcessAlive(pidB))
	}
}

func TestRunningTargetsOnlyIncludesAlive(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "live", "sleep 300", nil)
	mustAdd(t, m, "dead", "sleep 300", nil)
	if _, err := m.StartProcess("live"); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("live", true) })
	targets := m.runningTargets()
	if len(targets) != 1 || targets[0].name != "live" {
		t.Fatalf("runningTargets = %+v, want only live", targets)
	}
}

func TestPidLinePatternExtractsPid(t *testing.T) {
	sample := "{\n\t\"Label\" = \"com.darrenoakey.auto\";\n\t\"PID\" = 1061;\n}"
	match := pidLinePattern.FindSubmatch([]byte(sample))
	if match == nil || string(match[1]) != "1061" {
		t.Fatalf("failed to extract pid from sample, match=%v", match)
	}
}

func TestLaunchAgentPathSuffix(t *testing.T) {
	want := filepath.Join("LaunchAgents", LaunchAgentLabel+".plist")
	if got := LaunchAgentPath(); filepath.Join(filepath.Base(filepath.Dir(got)), filepath.Base(got)) != want {
		t.Fatalf("LaunchAgentPath = %s, want suffix %s", got, want)
	}
}

func TestAutoDaemonPidNonNegative(t *testing.T) {
	if AutoDaemonPid() < 0 {
		t.Fatal("AutoDaemonPid should never be negative")
	}
}
