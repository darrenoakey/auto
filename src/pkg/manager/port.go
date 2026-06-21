package manager

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// isPortFree reports whether a TCP port can be bound on localhost.
func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// killPortHolders finds every process listening on a port via lsof and SIGKILLs
// their entire process group (falling back to the bare pid). Returns the pids it
// signalled.
func killPortHolders(port int) []int {
	out, err := exec.Command("lsof", "-ti", ":"+strconv.Itoa(port)).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	killed := make([]int, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid, perr := strconv.Atoi(strings.TrimSpace(line))
		if perr != nil {
			continue
		}
		if killProcessGroup(pid, syscall.SIGKILL) {
			killed = append(killed, pid)
		}
	}
	return killed
}

// killProcessGroup signals the process group of pid, falling back to the bare
// pid. Returns whether any signal was delivered.
func killProcessGroup(pid int, sig syscall.Signal) bool {
	if pgid, err := syscall.Getpgid(pid); err == nil {
		if syscall.Kill(-pgid, sig) == nil {
			return true
		}
	}
	return syscall.Kill(pid, sig) == nil
}

// waitForPortFree polls until a port is free or the timeout elapses.
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

// forceFreePort repeatedly kills everything on a port until it is free or the
// attempts are exhausted.
func forceFreePort(port int) bool {
	for i := 0; i < 5; i++ {
		if isPortFree(port) {
			return true
		}
		killPortHolders(port)
		if waitForPortFree(port, 2*time.Second) {
			return true
		}
	}
	return isPortFree(port)
}
