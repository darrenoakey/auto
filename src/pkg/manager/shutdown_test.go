package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestShutdownUnloadsIsolatedKeepAliveJob(t *testing.T) {
	m := newTestManager(t)
	name := fmt.Sprintf("auto-shutdown-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	startShutdownTestIsolate(t, m, name)
	m.StopAllForRespawn()
	if !isolateBootstrapped(name) {
		t.Fatal("stop-all unregistered isolated job")
	}
	m.InhibitStarts()
	m.ShutdownAll()
	if isolateBootstrapped(name) {
		t.Fatal("isolated KeepAlive job still registered after shutdown")
	}
	if m.isExplicitlyStopped(name) {
		t.Fatal("shutdown persisted an operator stop")
	}
	if _, err := os.Stat(isolatePlistPath(name)); err != nil {
		t.Fatalf("shutdown removed next-login plist: %v", err)
	}
}

func startShutdownTestIsolate(t *testing.T, m *Manager, name string) {
	t.Helper()
	mustAdd(t, m, name, "/bin/sleep 300", nil)
	if err := m.SetIsolate(name, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.isolateRemoveArtifacts(name) })
	if _, err := m.StartProcess(name); err != nil {
		t.Fatal(err)
	}
}
