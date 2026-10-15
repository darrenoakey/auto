package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShimExecutesCommandWhenKernelAllows(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "auto-shim")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v: %s", err, output)
	}
	output, err := exec.Command(binary, "/bin/echo shutdown-gate-open").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "shutdown-gate-open" {
		t.Fatalf("shim command: %v: %s", err, output)
	}
}
