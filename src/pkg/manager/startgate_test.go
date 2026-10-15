package manager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemShutdownRequestedMatchesKernel(t *testing.T) {
	got, err := SystemShutdownRequested()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.willshutdown").Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(raw)) != "0"; got != want {
		t.Fatalf("shutdown = %v, kernel = %s", got, raw)
	}
}

func TestInhibitStartsBlocksLaunchPaths(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	m.InhibitStarts()
	for _, start := range []func(string) (int, error){m.StartProcess, m.RestartProcess, m.RestartProcessOverlap, m.RestartProcessStrictOverlap} {
		if _, err := start("svc"); !errors.Is(err, ErrStartsInhibited) {
			t.Fatalf("start after inhibition = %v", err)
		}
	}
	if _, _, _, err := m.spawnOnce("svc", "exec sleep 300", ""); !errors.Is(err, ErrStartsInhibited) {
		t.Fatalf("final spawn boundary = %v", err)
	}
	if err := m.isolateLaunch("svc", "sleep 300", ""); !errors.Is(err, ErrStartsInhibited) {
		t.Fatalf("isolated launch boundary = %v", err)
	}
	m.WatchTick()
	if _, alive := m.Status("svc"); alive {
		t.Fatal("inhibited watch tick started service")
	}
}

func TestShutdownDrainsInFlightSpawnWithoutPersistingStop(t *testing.T) {
	m := newTestManager(t)
	ready := filepath.Join(m.Root(), "ready")
	mustAdd(t, m, "svc", fmt.Sprintf("/bin/sh -c 'echo ready > %s; exec sleep 300'", ready), nil)
	t.Cleanup(m.ShutdownAll)
	started := make(chan error, 1)
	go func() { _, err := m.StartProcess("svc"); started <- err }()
	awaitFile(t, ready)
	m.InhibitStarts()
	m.ShutdownAll()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if _, alive := m.Status("svc"); alive || m.isExplicitlyStopped("svc") {
		t.Fatal("shutdown left service alive or persisted an operator stop")
	}
	assertFreshManagerCanStart(t, m.Root())
}

func assertFreshManagerCanStart(t *testing.T, root string) {
	t.Helper()
	fresh := New(root)
	if _, err := fresh.StartProcess("svc"); err != nil {
		t.Fatalf("fresh supervisor cannot resume: %v", err)
	}
	t.Cleanup(fresh.ShutdownAll)
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("child did not create %s", path)
		}
	}
}
