package manager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpawnWithRetrySurvivor(t *testing.T) {
	m := newTestManager(t)
	pid, logPath, err := m.spawnWithRetry("svc", "sleep 300", "", nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !isProcessAlive(pid) {
		t.Fatalf("survivor pid %d should be alive", pid)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log path should exist: %v", err)
	}
}

func TestSpawnWithRetryFastExitHandedBack(t *testing.T) {
	m := newTestManager(t)
	pid, _, err := m.spawnWithRetry("svc", "true", "", nil)
	if err != nil {
		t.Fatalf("fast-exit non-transient should be handed back without error, got %v", err)
	}
	if isProcessAlive(pid) {
		t.Fatalf("pid %d should have exited", pid)
	}
}

// TestSpawnUnsetWorkdirPinsRoot pins the determinism fix: a service with no
// workdir runs from the filesystem root regardless of the invoking process's
// cwd, so cwd-based attribution can never mislabel it.
func TestSpawnUnsetWorkdirPinsRoot(t *testing.T) {
	restore, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })
	m := newTestManager(t)
	logPath := spawnPwd(t, m, "svc-pwd-root", "")
	if got := strings.TrimSpace(readSpawnLog(t, logPath)); got != "/" {
		t.Fatalf("pwd = %q, want /", got)
	}
}

// TestSpawnExplicitWorkdirRunsThere confirms a stored workdir is honoured at
// spawn time, so the service's cwd is always the configured directory.
func TestSpawnExplicitWorkdirRunsThere(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	logPath := spawnPwd(t, m, "svc-pwd-dir", dir)
	if got := strings.TrimSpace(readSpawnLog(t, logPath)); got != dir {
		t.Fatalf("pwd = %q, want %q", got, dir)
	}
}

// spawnPwd runs `pwd` under the manager with the given workdir and returns
// the per-service log path the child wrote to.
func spawnPwd(t *testing.T, m *Manager, name, workdir string) string {
	t.Helper()
	_, logPath, err := m.spawnWithRetry(name, "pwd", workdir, nil)
	if err != nil {
		t.Fatalf("spawn %s: %v", name, err)
	}
	return logPath
}

// readSpawnLog waits briefly for the child's output to reach the log file,
// then returns its contents from the spawn offset onward.
func readSpawnLog(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read log: %v", err)
		}
		if strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("log %s stayed empty", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogHasTransientExecError(t *testing.T) {
	dir := t.TempDir()
	withMarker := filepath.Join(dir, "bad.log")
	if err := os.WriteFile(withMarker, []byte("zsh: Resource deadlock avoided\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !logHasTransientExecError(withMarker, 0) {
		t.Fatal("should detect transient marker")
	}
	clean := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(clean, []byte("listening on :8080\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if logHasTransientExecError(clean, 0) {
		t.Fatal("clean log should not match")
	}
}

func TestLogHasTransientExecErrorRespectsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "day.log")
	// A transient marker from an earlier spawn the same day ...
	stale := []byte("zsh: Resource deadlock avoided\n")
	if err := os.WriteFile(path, stale, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// ... followed by this spawn's clean output.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("listening on :8080\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	// Inspecting from the offset of this spawn should NOT see the stale marker.
	if logHasTransientExecError(path, int64(len(stale))) {
		t.Fatal("stale marker before offset should not be detected")
	}
	// But a full-file read (offset 0) still sees it.
	if !logHasTransientExecError(path, 0) {
		t.Fatal("full-file read should still detect the stale marker")
	}
}

func TestLogHasAddressInUseMatchesRuntimeVariants(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		text string
	}{
		{"go-bsd", "listen tcp 127.0.0.1:8420: bind: address already in use\n"},
		{"python", "OSError: [Errno 48] Address already in use\n"},
		{"node", "Error: listen EADDRINUSE: address already in use :::8420\n"},
		{"rust", "Os { code: 48, kind: AddrInUse, message: \"Address already in use\" }\n"},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.name+".log")
		if err := os.WriteFile(path, []byte(c.text), 0o644); err != nil {
			t.Fatalf("write %s: %v", c.name, err)
		}
		if !logHasAddressInUse(path, 0) {
			t.Fatalf("%s: should detect address-in-use marker in %q", c.name, c.text)
		}
	}
	clean := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(clean, []byte("listening on :8420\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if logHasAddressInUse(clean, 0) {
		t.Fatal("clean log should not match address-in-use")
	}
}

func TestLogHasAddressInUseRespectsOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "day.log")
	stale := []byte("Address already in use\n")
	if err := os.WriteFile(path, stale, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("listening on :8420\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	if logHasAddressInUse(path, int64(len(stale))) {
		t.Fatal("stale marker before offset should not be detected")
	}
	if !logHasAddressInUse(path, 0) {
		t.Fatal("full-file read should still detect the stale marker")
	}
}

// TestRestartProcessRefusesPortInterloper pins the refusal side of the port
// policy end to end: when an unmanaged process steals the service's port in
// the TOCTOU gap between the free-port check and the child's own bind, the
// restart must fail cleanly naming the policy, the interloper must survive
// untouched and keep holding the port, the owned previous instance must still
// have been stopped, and a later restart must converge once the port is
// genuinely free — all without ever signalling the interloper.
func TestRestartProcessRefusesPortInterloper(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatalf("python3 is required for the real listener test: %v", err)
	}
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	bindCmd := listenerCommand(port, false)
	mustAdd(t, m, "svc", bindCmd, &port)
	first, err := m.StartProcess("svc")
	if err != nil {
		t.Fatalf("initial start: %v", err)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("managed listener did not bind its configured port")
	}

	interloperCmd := listenerCommand(port, false)
	interloperPid := 0
	afterPortCheckHook = func(hookPort int) {
		if hookPort != port {
			return
		}
		cmd := exec.Command("/bin/bash", "-c", interloperCmd)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Errorf("failed to start interloper: %v", err)
			return
		}
		interloperPid = cmd.Process.Pid
		go func() { _, _ = cmd.Process.Wait() }()
		// Block until the interloper has actually grabbed the port, so the real
		// spawn is guaranteed to lose the bind race deterministically.
		if !waitForPortHeld(port, 2*time.Second) {
			t.Errorf("interloper did not bind port %d", port)
		}
	}
	t.Cleanup(func() { afterPortCheckHook = nil })

	if _, err := m.RestartProcess("svc"); err == nil {
		t.Fatal("restart must refuse while an unmanaged interloper holds the port")
	} else if !strings.Contains(err.Error(), "never kills unmanaged port holders") {
		t.Fatalf("refusal should name the policy, got: %v", err)
	}
	if interloperPid == 0 {
		t.Fatal("interloper must have started inside the TOCTOU hook")
	}
	if !isProcessAlive(interloperPid) {
		t.Fatalf("unmanaged interloper %d must remain alive after the refused restart", interloperPid)
	}
	if !waitForPortHeld(port, time.Second) {
		t.Fatal("interloper must still hold the port after the refused restart")
	}
	if isProcessAlive(first) {
		t.Fatalf("owned previous instance %d must still have been stopped", first)
	}

	// Only the test ends its own interloper; auto never touched it.
	if err := syscall.Kill(-interloperPid, syscall.SIGKILL); err != nil {
		t.Fatalf("killing test interloper: %v", err)
	}
	if !waitForPortFree(port, 5*time.Second) {
		t.Fatal("port should free once the interloper is gone")
	}
	afterPortCheckHook = nil
	second, err := m.RestartProcess("svc")
	if err != nil {
		t.Fatalf("restart should converge once the port is genuinely free: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	if second == first {
		t.Fatalf("restart should yield a new pid, both %d", first)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("port should be held by the restarted service")
	}
	if !endpointResponds(port) {
		t.Fatal("restarted service should answer connections on its port")
	}
	if pid, alive := m.Status("svc"); !alive || pid != second {
		t.Fatalf("state should track the restarted service, got (%d,%v)", pid, alive)
	}
}

func TestIsTransientSpawnError(t *testing.T) {
	if !isTransientSpawnError(syscall.EAGAIN) {
		t.Fatal("EAGAIN should be transient")
	}
	if isTransientSpawnError(syscall.ENOENT) {
		t.Fatal("ENOENT should not be transient")
	}
}

// TestSpawnAddressInUseWithoutPortRefusesInsteadOfRetrying pins the busy-loop
// fix with a real standing occupier: a service whose registry row carries no
// port really loses its bind to a real listener. auto cannot probe an address
// it was never told about, so it must refuse rather than assume the occupier
// is momentary — and above all it must spawn the doomed child ONCE, not
// SpawnRetryAttempts times, every supervision cycle for as long as the
// occupier lives.
func TestSpawnAddressInUseWithoutPortRefusesInsteadOfRetrying(t *testing.T) {
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	startUnownedListener(t, port)
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("unmanaged listener did not bind the port the service will lose")
	}

	_, _, err := m.spawnWithRetry("svc-no-port", listenerCommand(port, false), "", nil)
	if err == nil {
		t.Fatal("spawn must refuse when the child reports address already in use and no port is configured")
	}
	if !strings.Contains(err.Error(), "no port is configured") {
		t.Fatalf("refusal should name the missing port configuration, got: %v", err)
	}

	logPath := m.dailyLogPath("svc-no-port")
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read service log: %v", readErr)
	}
	binds := strings.Count(strings.ToLower(string(data)), "address already in use")
	if binds != 1 {
		t.Fatalf("child should have been spawned exactly once, but its log records %d failed binds:\n%s", binds, data)
	}
}

// TestSpawnAddressInUseWithConfiguredPortStillRefusesStandingHolder keeps the
// configured-port branch honest alongside the no-port refusal above: when the
// port IS known and a real process still holds it, the refusal names the
// no-kill policy rather than the missing configuration.
func TestSpawnAddressInUseWithConfiguredPortStillRefusesStandingHolder(t *testing.T) {
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	startUnownedListener(t, port)
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("unmanaged listener did not bind its port")
	}

	_, _, err := m.spawnWithRetry("svc-with-port", listenerCommand(port, false), "", &port)
	if err == nil {
		t.Fatal("spawn must refuse while a real process holds the configured port")
	}
	if !strings.Contains(err.Error(), "never kills unmanaged port holders") {
		t.Fatalf("refusal should name the no-kill policy, got: %v", err)
	}
}
