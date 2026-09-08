package manager

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIsPortFreeReflectsBinding(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if isPortFree(port) {
		t.Fatalf("port %d is bound, should not be free", port)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !waitForPortFree(port, 2*time.Second) {
		t.Fatalf("port %d should be free after close", port)
	}
}

// TestStartRefusesUnownedPortHolder pins the port-refusal policy end to end:
// an unmanaged process that already holds a configured port causes a clean
// refusal naming the policy, is never signalled, and still holds the port and
// answers connections afterwards.
func TestStartRefusesUnownedPortHolder(t *testing.T) {
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	holder := startUnownedListener(t, port)
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("unmanaged listener did not bind its configured port")
	}
	mustAdd(t, m, "svc", "sleep 300", &port)
	if _, err := m.StartProcess("svc"); err == nil {
		t.Fatal("start must refuse while an unmanaged process holds the configured port")
	} else if !strings.Contains(err.Error(), "never kills unmanaged port holders") {
		t.Fatalf("refusal should name the policy, got: %v", err)
	}
	if !isProcessAlive(holder) {
		t.Fatalf("unmanaged port holder %d must remain alive after the refused start", holder)
	}
	if !waitForPortHeld(port, time.Second) {
		t.Fatal("unmanaged port holder must still hold the port after the refused start")
	}
	if !endpointResponds(port) {
		t.Fatal("unmanaged port holder must still answer connections")
	}
}

// TestStopFreesPortForRestart pins the healthy stop/restart contract with a
// real listener: an owned stop releases the configured port without any
// port-scoped killing, and a fresh start then binds and serves it again.
func TestStopFreesPortForRestart(t *testing.T) {
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	mustAdd(t, m, "holder", listenerCommand(port, false), &port)
	first, err := m.StartProcess("holder")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("managed listener did not bind its configured port")
	}
	if err := m.StopProcess("holder", true); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if isProcessAlive(first) {
		t.Fatalf("owned process %d must be dead after stop", first)
	}
	if !waitForPortFree(port, PortReleaseWait+2*time.Second) {
		t.Fatal("owned stop must release the configured port")
	}
	second, err := m.StartProcess("holder")
	if err != nil {
		t.Fatalf("restart after clean stop: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("holder", true) })
	if second == first {
		t.Fatalf("restart should yield a new pid, both %d", first)
	}
	if !waitForPortHeld(port, 3*time.Second) {
		t.Fatal("restarted service should hold its port again")
	}
	if !endpointResponds(port) {
		t.Fatal("restarted service should answer connections on its port")
	}
}

// startUnownedListener spawns a real detached listener that auto does not own,
// returning its pid. Cleanup terminates only the test's own child; the point
// of the tests using it is to prove auto left it running while it mattered.
func startUnownedListener(t *testing.T, port int) int {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatalf("python3 is required for the real listener test: %v", err)
	}
	cmd := exec.Command("/bin/bash", "-c", "exec "+listenerCommand(port, false))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	pid := cmd.Process.Pid
	go func() { _, _ = cmd.Process.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	return pid
}

// listenerCommand starts a real TCP listener that remains live without stdin.
func listenerCommand(port int, reuseAddress bool) string {
	reuse := ""
	if reuseAddress {
		reuse = "s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); "
	}
	return fmt.Sprintf(`python3 -c "import socket,time; s=socket.socket(socket.AF_INET, socket.SOCK_STREAM); %ss.bind(('127.0.0.1', %d)); s.listen(5); time.sleep(300)"`, reuse, port)
}

// endpointResponds proves a live listener answers on a TCP port by connecting
// to it — a real endpoint probe, never a port-to-pid table scan.
func endpointResponds(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// freeEphemeralPort returns a currently-free localhost port.
func freeEphemeralPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// waitForPortHeld polls until a port is in use or the timeout elapses.
func waitForPortHeld(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isPortFree(port) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !isPortFree(port)
}
