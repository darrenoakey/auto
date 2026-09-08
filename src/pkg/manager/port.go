package manager

import (
	"net"
	"strconv"
	"syscall"
	"time"
)

// isPortFree reports whether a TCP port can be bound on localhost. This is an
// actual endpoint probe: a real bind against the kernel, not a table scan, so
// it can never attribute or disturb whoever holds the port.
func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// killProcessGroup signals the process group of pid, falling back to the bare
// pid. It is used exclusively for OWNED managed processes and their children:
// the pid comes from retained per-service state, never from a port or
// process-table lookup, so an unrelated process can never be matched this way.
func killProcessGroup(pid int, sig syscall.Signal) bool {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		if syscall.Kill(-pgid, sig) == nil {
			return true
		}
	}
	return syscall.Kill(pid, sig) == nil
}

// waitForPortFree polls the endpoint probe until a port is free or the
// timeout elapses.
func waitForPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isPortFree(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return isPortFree(port)
}
