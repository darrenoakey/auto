package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestShutdownEscalatesChildAfterLeaderExits(t *testing.T) {
	m := newTestManager(t)
	childFile := filepath.Join(m.Root(), "child")
	command := fmt.Sprintf(`/bin/sh -c 'trap "exit 0" TERM; /bin/sh -c '\''trap "" TERM; echo $$ > %s; exec sleep 300'\'' & wait'`, childFile)
	mustAdd(t, m, "tree", command, nil)
	leader, err := m.StartProcess("tree")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	awaitFile(t, childFile)
	child := readChildPid(t, childFile)
	if !processGroupAlive(leader) {
		t.Fatal("live process group not found")
	}
	m.InhibitStarts()
	m.ShutdownAll()
	if isProcessAlive(child) || processGroupAlive(leader) {
		t.Fatalf("shutdown left child %d alive after leader exited", child)
	}
}

func readChildPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestProcessGroupAliveRejectsInvalidGroup(t *testing.T) {
	if processGroupAlive(0) || processGroupAlive(-1) {
		t.Fatal("invalid process group reported alive")
	}
}
