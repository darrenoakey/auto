package manager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddProcessDuplicateFails(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if err := m.AddProcess("svc", "sleep 2", nil, ""); err == nil {
		t.Fatal("adding duplicate should fail")
	}
}

func TestAddProcessRejectsBadPort(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess("svc", "sleep 1", intPtr(70000), ""); err == nil {
		t.Fatal("out-of-range port should fail")
	}
}

// TestAddProcessNeverCapturesInvokerCwd pins the fix for silent cwd capture:
// an add without a workdir stores an empty workdir even when auto runs from
// some other directory. Before this, a service added from the agentd3 repo
// inherited it as workdir, and every monitor that labels processes by cwd
// misattributed the service's children to that repo.
func TestAddProcessNeverCapturesInvokerCwd(t *testing.T) {
	m := newTestManager(t)
	restore, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })
	mustAdd(t, m, "svc", "sleep 1", nil)
	def, _ := m.definition("svc")
	if def.Workdir != "" {
		t.Fatalf("workdir = %q, want empty (no cwd capture)", def.Workdir)
	}
}

// TestAddProcessResolvesExplicitWorkdir confirms an explicit workdir is
// stored absolute and a missing directory fails loudly at add time.
func TestAddProcessResolvesExplicitWorkdir(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	if err := m.AddProcess("svc", "sleep 1", nil, dir); err != nil {
		t.Fatalf("add: %v", err)
	}
	def, _ := m.definition("svc")
	if def.Workdir != dir {
		t.Fatalf("workdir = %q, want %q", def.Workdir, dir)
	}
	if err := m.AddProcess("svc2", "sleep 1", nil, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing workdir should fail")
	}
}

// TestGetWorkdir reports the stored workdir, empty when unset, and an error
// for an unknown process.
func TestGetWorkdir(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	wd, err := m.GetWorkdir("svc")
	if err != nil || wd != "" {
		t.Fatalf("GetWorkdir = %q, %v; want empty, nil", wd, err)
	}
	if _, err := m.GetWorkdir("nope"); err == nil {
		t.Fatal("unknown process should error")
	}
}

func TestUpdateProcessChangesFields(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	newCmd := "sleep 2"
	if err := m.UpdateProcess("svc", &newCmd, intPtr(9090), nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	def, _ := m.definition("svc")
	if def.Command != newCmd || def.Port == nil || *def.Port != 9090 {
		t.Fatalf("update did not apply: %+v", def)
	}
}

// TestUpdateProcessEmptyWorkdirClears pins the clear semantic: an explicit
// empty workdir stores empty rather than resolving "" — which filepath.Abs
// turns into the updater's own cwd — back into the definition.
func TestUpdateProcessEmptyWorkdirClears(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	if err := m.AddProcess("svc", "sleep 1", nil, dir); err != nil {
		t.Fatalf("add: %v", err)
	}
	restore, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(restore) })
	empty := ""
	if err := m.UpdateProcess("svc", nil, nil, &empty); err != nil {
		t.Fatalf("update: %v", err)
	}
	def, _ := m.definition("svc")
	if def.Workdir != "" {
		t.Fatalf("workdir = %q, want cleared to empty", def.Workdir)
	}
}
func TestUpdateProcessMissingFails(t *testing.T) {
	m := newTestManager(t)
	if err := m.UpdateProcess("ghost", nil, nil, nil); err == nil {
		t.Fatal("updating missing process should fail")
	}
}

func TestRemoveProcessDeletes(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if err := m.RemoveProcess("svc"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := m.definition("svc"); ok {
		t.Fatal("process should be gone after remove")
	}
}

func TestListProcessesReportsDefinitions(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "alpha", "sleep 1", intPtr(8000))
	mustAdd(t, m, "beta", "sleep 2", nil)
	infos := m.ListProcesses()
	if len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "beta" {
		t.Fatalf("ListProcesses wrong: %+v", infos)
	}
	if infos[0].Port == nil || *infos[0].Port != 8000 {
		t.Fatalf("alpha port wrong: %+v", infos[0])
	}
}

func TestListProcessesSkipsCommandlessStubs(t *testing.T) {
	m := newTestManager(t)
	// A runtime-only stub (no command) alongside a real definition: only the
	// real one should be listed, matching the original load_config behaviour.
	seedState(t, m, `{"processes":{
		"real":{"command":"sleep 1"},
		"stub":{"pid":4242,"explicitly_stopped":false}
	}}`)
	infos := m.ListProcesses()
	if len(infos) != 1 || infos[0].Name != "real" {
		t.Fatalf("expected only 'real' listed, got %+v", infos)
	}
	if names := m.definedNames(); len(names) != 1 || names[0] != "real" {
		t.Fatalf("definedNames = %v, want [real]", names)
	}
}

func TestGetCommand(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 300", nil)
	cmd, err := m.GetCommand("svc")
	if err != nil || cmd != "sleep 300" {
		t.Fatalf("GetCommand = %q, %v", cmd, err)
	}
	if _, err := m.GetCommand("ghost"); err == nil {
		t.Fatal("missing command should error")
	}
}

func TestResolveExistingDirRejectsMissing(t *testing.T) {
	if _, err := resolveExistingDir("/no/such/dir/here/xyz"); err == nil {
		t.Fatal("missing dir should error")
	}
}
