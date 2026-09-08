package manager

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseLstartTimeLocaleVariants(t *testing.T) {
	us, okUS := parseLstartTime("Wed Jun 18 11:33:09 2026")
	au, okAU := parseLstartTime("Wed 18 Jun 11:33:09 2026")
	if !okUS || !okAU {
		t.Fatalf("parse failed: us=%v au=%v", okUS, okAU)
	}
	if !us.Equal(au) {
		t.Fatalf("US and AU forms differ: %v vs %v", us, au)
	}
}

func TestParseLstartTimeEmptyFails(t *testing.T) {
	if _, ok := parseLstartTime(""); ok {
		t.Fatal("empty string should not parse")
	}
}

func TestIsProcessAliveForSelf(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
}

func TestIsProcessAliveForDeadPid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	if isProcessAlive(cmd.Process.Pid) {
		t.Fatalf("reaped pid %d should be dead", cmd.Process.Pid)
	}
}

func TestIsOurProcessMatchesStartTime(t *testing.T) {
	pid := os.Getpid()
	st := processStartTime(pid)
	if st == "" {
		t.Skip("process start time unavailable in this environment")
	}
	if !isOurProcess(pid, &st) {
		t.Fatal("self pid with correct start time should match")
	}
	wrong := "Wed Jun 18 11:33:09 2000"
	if isOurProcess(pid, &wrong) {
		t.Fatal("mismatched start time should not match")
	}
	if isOurProcess(pid, nil) {
		t.Fatal("nil start time should be treated as stale")
	}
}

// TestProcessStartTimeMatchesPsGroundTruth pins the kern.proc.pid identity
// source against `ps -o lstart=` for real processes: the test process itself
// and a freshly spawned child must report the same start instant through the
// kernel sysctl and through ps. ps is used only as read-only ground truth for
// two targeted pids — never as a process-table scan.
func TestProcessStartTimeMatchesPsGroundTruth(t *testing.T) {
	for _, pid := range []int{os.Getpid(), childSleepPid(t)} {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
		if err != nil {
			t.Skipf("ps -p %d unavailable: %v", pid, err)
		}
		psStart := strings.TrimSpace(string(out))
		kernelStart := processStartTime(pid)
		if kernelStart == "" {
			t.Fatalf("kernel start time for live pid %d is missing", pid)
		}
		if !startTimesMatch(kernelStart, psStart) {
			t.Fatalf("pid %d: kernel start %q must equal ps lstart %q", pid, kernelStart, psStart)
		}
	}
}

// TestProcessStartTimeEmptyForReapedPid pins that a pid that no longer exists
// yields no start time, so a recycled pid can never inherit the old
// generation's identity.
func TestProcessStartTimeEmptyForReapedPid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	if got := processStartTime(cmd.Process.Pid); got != "" {
		t.Fatalf("reaped pid %d must have no start time, got %q", cmd.Process.Pid, got)
	}
}

// TestIsProcessAliveRejectsRealZombie drives a real zombie through the kernel:
// a killed-but-unreaped child still answers signal 0, so only the kernel's
// scheduler state may distinguish it from a live process.
func TestIsProcessAliveRejectsRealZombie(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	zombie := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := kernelProc(pid); err == nil && info.Proc.P_stat == procStateZombie {
			zombie = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !zombie {
		t.Skip("child was reaped before it could be observed as a zombie")
	}
	if isProcessAlive(pid) {
		t.Fatalf("zombie pid %d must not read as alive", pid)
	}
	_ = cmd.Wait() // reap; the kill's exit status is expected
	if isProcessAlive(pid) {
		t.Fatalf("reaped pid %d must not read as alive", pid)
	}
}

// childSleepPid spawns a real long-running child and returns its pid, with
// cleanup that terminates only the test's own child.
func childSleepPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	go func() { _, _ = cmd.Process.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}
