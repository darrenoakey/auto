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

// TestCmdAddWorkdirFlagPersistsExplicitWorkdir confirms `add workdir=<dir>`
// stores the explicit directory and the service starts from there.
func TestCmdAddWorkdirFlagPersistsExplicitWorkdir(t *testing.T) {
	m := manager.New(t.TempDir())
	dir := t.TempDir()
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	p := &parsedArgs{positional: []string{"svc", "sleep", "300"}, values: map[string]string{"workdir": dir}}
	if code := cmdAdd(m, p); code != 0 {
		t.Fatalf("cmdAdd workdir= = %d, want 0", code)
	}
	wd, err := m.GetWorkdir("svc")
	if err != nil || wd != dir {
		t.Fatalf("workdir = %q, %v; want %q", wd, err, dir)
	}
}

// TestCmdAddWithoutWorkdirStoresEmpty pins the no-capture behaviour at the
// CLI layer: an add with no workdir= never stores the invoker's cwd.
func TestCmdAddWithoutWorkdirStoresEmpty(t *testing.T) {
	m := manager.New(t.TempDir())
	t.Cleanup(func() { _ = m.StopProcess("svc", true) })
	p := &parsedArgs{positional: []string{"svc", "sleep", "300"}}
	if code := cmdAdd(m, p); code != 0 {
		t.Fatalf("cmdAdd = %d, want 0", code)
	}
	wd, err := m.GetWorkdir("svc")
	if err != nil || wd != "" {
		t.Fatalf("workdir = %q, %v; want empty", wd, err)
	}
}

// TestEffectiveWorkdir covers the display mapping: unset renders as / so add
// output and `show` always state where the service will actually run.
func TestEffectiveWorkdir(t *testing.T) {
	if got := effectiveWorkdir(""); got != "/" {
		t.Fatalf("effectiveWorkdir(\"\") = %q, want /", got)
	}
	if got := effectiveWorkdir("/tmp"); got != "/tmp" {
		t.Fatalf("effectiveWorkdir(\"/tmp\") = %q", got)
	}
}

// TestWarnRelativeCommandOnlyFiresWithoutWorkdir pins the guard: a relative
// command with no workdir warns (it will run from /); anything else stays
// quiet. Output is asserted via the pure predicate, not stdout capture.
func TestWarnRelativeCommandOnlyFiresWithoutWorkdir(t *testing.T) {
	cases := []struct {
		name, command, workdir string
		want                   bool
	}{
		{"relative no workdir", "./run serve", "", true},
		{"relative with workdir", "./run serve", "/tmp", false},
		{"absolute no workdir", "/bin/sleep 1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandNeedsWorkdir(tc.command, tc.workdir); got != tc.want {
				t.Fatalf("commandNeedsWorkdir(%q, %q) = %v, want %v", tc.command, tc.workdir, got, tc.want)
			}
		})
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
