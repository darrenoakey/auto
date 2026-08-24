package main

import (
	"testing"

	"auto/pkg/manager"
)

func TestOptPort(t *testing.T) {
	got, err := optPort(&parsedArgs{values: map[string]string{"port": "8080"}})
	if err != nil || got == nil || *got != 8080 {
		t.Fatalf("optPort = %v, %v", got, err)
	}
	if v, err := optPort(&parsedArgs{values: map[string]string{}}); err != nil || v != nil {
		t.Fatalf("absent port should be nil, got %v, %v", v, err)
	}
	if _, err := optPort(&parsedArgs{values: map[string]string{"port": "x"}}); err == nil {
		t.Fatal("non-integer port should error")
	}
}

func TestOptStr(t *testing.T) {
	p := &parsedArgs{values: map[string]string{"command": "sleep 1"}}
	if got := optStr(p, "command"); got == nil || *got != "sleep 1" {
		t.Fatalf("optStr = %v", got)
	}
	if optStr(p, "workdir") != nil {
		t.Fatal("absent value should be nil")
	}
}

// TestCmdUpdateIsolateFlagAppliesSetIsolate confirms `update --isolate=on`
// marks the process launchd-managed without starting it (cmdUpdate never
// spawns), so this stays a pure state-file assertion with no real launchctl
// call. `--isolate=off` reverses it the same way.
func TestCmdUpdateIsolateFlagAppliesSetIsolate(t *testing.T) {
	m := manager.New(t.TempDir())
	if err := m.AddProcess("svc", "sleep 300", nil, ""); err != nil {
		t.Fatalf("AddProcess: %v", err)
	}
	on := &parsedArgs{positional: []string{"svc"}, values: map[string]string{"isolate": "on"}}
	if code := cmdUpdate(m, on); code != 0 {
		t.Fatalf("cmdUpdate --isolate=on = %d, want 0", code)
	}
	infos := m.ListProcesses()
	if len(infos) != 1 || !infos[0].Isolate {
		t.Fatalf("update --isolate=on did not persist: %+v", infos)
	}
	off := &parsedArgs{positional: []string{"svc"}, values: map[string]string{"isolate": "off"}}
	if code := cmdUpdate(m, off); code != 0 {
		t.Fatalf("cmdUpdate --isolate=off = %d, want 0", code)
	}
	infos = m.ListProcesses()
	if len(infos) != 1 || infos[0].Isolate {
		t.Fatalf("update --isolate=off did not clear: %+v", infos)
	}
}

// TestCmdUpdateIsolateRejectsBadValue confirms an unrecognized --isolate
// value fails loudly rather than silently doing nothing.
func TestCmdUpdateIsolateRejectsBadValue(t *testing.T) {
	m := manager.New(t.TempDir())
	if err := m.AddProcess("svc", "sleep 300", nil, ""); err != nil {
		t.Fatalf("AddProcess: %v", err)
	}
	p := &parsedArgs{positional: []string{"svc"}, values: map[string]string{"isolate": "sideways"}}
	if code := cmdUpdate(m, p); code == 0 {
		t.Fatal("cmdUpdate --isolate=sideways should fail")
	}
}
