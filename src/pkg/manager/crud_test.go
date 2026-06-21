package manager

import "testing"

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

func TestAddProcessDefaultsWorkdirToCwd(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	def, _ := m.definition("svc")
	if def.Workdir == "" {
		t.Fatal("workdir should default to a real directory")
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
