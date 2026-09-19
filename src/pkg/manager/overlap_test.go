package manager

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// helperBin is the compiled porthelper test service (see
// testdata/porthelper/main.go), built once per test-binary run so every
// overlap-restart test spawns a deterministic fast-starting child.
var helperBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "auto-porthelper")
	if err != nil {
		fmt.Fprintf(os.Stderr, "porthelper tempdir: %v\n", err)
		os.Exit(2)
	}
	bin := filepath.Join(dir, "porthelper")
	build := exec.Command("go", "build", "-o", bin, "./testdata/porthelper")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "porthelper build: %v\n%s\n", err, output)
		os.Exit(2)
	}
	helperBin = bin
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// freePort reserves an OS-assigned TCP port and releases it for the test's
// service to bind.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return port
}

// holderCommand registers and starts a porthelper service on port, returning
// its pid.
func holderCommand(port int, reuse bool) string {
	if reuse {
		return fmt.Sprintf("%s -reuse -port %d", helperBin, port)
	}
	return fmt.Sprintf("%s -port %d", helperBin, port)
}

func startHolder(t *testing.T, m *Manager, name string, port int, reuse bool) int {
	t.Helper()
	mustAdd(t, m, name, holderCommand(port, reuse), intPtr(port))
	pid, err := m.StartProcess(name)
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return pid
}

// waitForServing polls the port with real dials until it accepts, proving the
// full listen path works rather than probing for a listener socket.
func waitForServing(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d never accepted connections", port)
}

func TestRestartProcessOverlapServesWithoutRefusalWindow(t *testing.T) {
	m := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, m, "tunnel", port, true)
	t.Cleanup(func() { killGroup(t, oldPid) })
	waitForServing(t, port)

	type overlapResult struct {
		pid int
		err error
	}
	done := make(chan overlapResult, 1)
	go func() {
		pid, err := m.RestartProcessOverlap("tunnel")
		done <- overlapResult{pid: pid, err: err}
	}()

	// Dial continuously across the whole swap: the replacement binds beside
	// the old instance before the old one is terminated, so every dial must
	// be served by one of the two.
	refusals := 0
	probes := 0
	drainAndCheck := func(r overlapResult) {
		if r.err != nil {
			t.Fatalf("overlap restart: %v", r.err)
		}
		if r.pid == oldPid {
			t.Fatal("overlap restart returned the old pid")
		}
	}
loop:
	for {
		select {
		case r := <-done:
			drainAndCheck(r)
			break loop
		default:
		}
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
		probes++
		if err != nil {
			refusals++
		} else {
			_ = connection.Close()
		}
		time.Sleep(2 * time.Millisecond)
		if probes > 20000 {
			t.Fatal("overlap restart did not finish")
		}
	}
	if refusals > 0 {
		t.Fatalf("%d of %d dials were refused during overlap restart", refusals, probes)
	}
	waitForServing(t, port)
	pid, alive := m.Status("tunnel")
	if !alive || pid == oldPid {
		t.Fatalf("Status = (%d, %v); want the replacement pid serving", pid, alive)
	}
}

func TestRestartProcessOverlapFallsBackToSerialWhenBindCannotReuse(t *testing.T) {
	m := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, m, "tunnel", port, false)
	t.Cleanup(func() { killGroup(t, oldPid) })
	waitForServing(t, port)

	// The running listener has no SO_REUSEPORT, so the replacement's bind
	// fails deterministically and the overlap path must converge through the
	// classic serial restart instead of leaving the service dead.
	pid, err := m.RestartProcessOverlap("tunnel")
	if err != nil {
		t.Fatalf("overlap restart with non-reuse listener: %v", err)
	}
	if pid == oldPid {
		t.Fatal("restart returned the old pid")
	}
	if isProcessAlive(oldPid) {
		t.Fatal("old instance survived the fallback restart")
	}
	waitForServing(t, port)
	got, alive := m.Status("tunnel")
	if !alive || got != pid {
		t.Fatalf("Status = (%d, %v); want (%d, true)", got, alive, pid)
	}
}

func TestRestartProcessOverlapDeadProcessTakesSerialPath(t *testing.T) {
	m := newTestManager(t)
	port := freePort(t)
	oldPid := startHolder(t, m, "tunnel", port, true)
	t.Cleanup(func() { killGroup(t, oldPid) })
	waitForServing(t, port)

	killGroup(t, oldPid)
	waitForActualProcessDeath(t, oldPid)

	pid, err := m.RestartProcessOverlap("tunnel")
	if err != nil {
		t.Fatalf("overlap restart of dead process: %v", err)
	}
	if pid == oldPid {
		t.Fatal("restart returned the dead pid")
	}
	waitForServing(t, port)
	got, alive := m.Status("tunnel")
	if !alive || got != pid {
		t.Fatalf("Status = (%d, %v); want (%d, true)", got, alive, pid)
	}
}
