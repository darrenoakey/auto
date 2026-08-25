package manager

import (
	"fmt"
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

// TestRestartProcessConvergesThroughPortInterloper reproduces the deploy race
// end-to-end: something briefly re-grabs a service's port in the exact gap
// between StartProcess confirming the port free and the new process's own
// bind() call. Without the fix, the new process's bind fails, it exits, and
// the caller's crash-restart backoff would be needed to eventually recover
// (a client-visible connection-refused window). With the fix, spawnWithRetry
// recognizes the address-in-use marker, forces the interloper off the port,
// and retries within this single RestartProcess call.
func TestRestartProcessConvergesThroughPortInterloper(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	bindCmd := fmt.Sprintf(
		`python3 -c "import socket,time; s=socket.socket(socket.AF_INET, socket.SOCK_STREAM); s.bind(('127.0.0.1', %d)); s.listen(1); time.sleep(300)"`,
		port)
	mustAdd(t, m, "svc", bindCmd, &port)
	first, err := m.StartProcess("svc")
	if err != nil {
		t.Fatalf("initial start: %v", err)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Skip("python3 did not bind the port (environment differs)")
	}

	interloperCmd := fmt.Sprintf(
		`exec python3 -c "import socket,time; s=socket.socket(socket.AF_INET, socket.SOCK_STREAM); `+
			`s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(('127.0.0.1', %d)); s.listen(1); time.sleep(30)"`,
		port)
	afterPortCheckHook = func(hookPort int) {
		if hookPort != port {
			return
		}
		cmd := exec.Command("/bin/sh", "-c", interloperCmd)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			t.Errorf("failed to start interloper: %v", err)
			return
		}
		go func() { _, _ = cmd.Process.Wait() }()
		// Block until the interloper has actually grabbed the port, so the real
		// spawn is guaranteed to lose the bind race deterministically.
		waitForPortHeld(port, 2*time.Second)
	}
	t.Cleanup(func() { afterPortCheckHook = nil })

	start := time.Now()
	second, err := m.RestartProcess("svc")
	elapsed := time.Since(start)
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	if err != nil {
		t.Fatalf("restart should converge despite the port interloper, got error: %v", err)
	}
	if second == first {
		t.Fatalf("restart should yield a new pid, both %d", first)
	}
	// The interloper sleeps for 30s; converging well inside that window proves
	// forceFreePort actively reclaimed the port rather than the test merely
	// waiting out the interloper's own exit.
	if elapsed > 10*time.Second {
		t.Fatalf("restart took %s to converge, want well under the interloper's 30s hold", elapsed)
	}
	if !isProcessAlive(second) {
		t.Fatalf("new pid %d should be alive", second)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("port should be held by the restarted service")
	}
	holders := lsofPortPids(port)
	if len(holders) != 1 || holders[0] != second {
		t.Fatalf("expected exactly pid %d holding port %d, got %v", second, port, holders)
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
