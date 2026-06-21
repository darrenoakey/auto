package manager

import (
	"os"
	"os/exec"
	"testing"
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
		t.Skip("ps lstart unavailable in this environment")
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
