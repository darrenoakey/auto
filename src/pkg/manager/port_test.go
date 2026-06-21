package manager

import (
	"net"
	"os/exec"
	"strconv"
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

func TestForceFreePortKillsHolder(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc unavailable")
	}
	m := newTestManager(t)
	port := freeEphemeralPort(t)
	mustAdd(t, m, "holder", "exec nc -l "+strconv.Itoa(port), nil)
	if _, err := m.StartProcess("holder"); err != nil {
		t.Fatalf("starting holder: %v", err)
	}
	t.Cleanup(func() { _ = m.StopProcess("holder", true) })
	if !waitForPortHeld(port, 3*time.Second) {
		t.Skip("nc did not bind the port (variant differs)")
	}
	if !forceFreePort(port) {
		t.Fatalf("forceFreePort did not reclaim port %d", port)
	}
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
