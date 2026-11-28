package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProtectedStopRequiresAttribution verifies an unattended stop of the
// protected service is rejected and the service is never signalled.
func TestProtectedStopRequiresAttribution(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess(ProtectedService, "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	pid, err := m.StartProcess(ProtectedService)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = m.StopProcess(ProtectedService, false) }()

	if err := m.StopProcessMaintenance(ProtectedService, StopRequest{}); err == nil {
		t.Fatal("expected unattended explicit stop of protected service to fail")
	}
	if _, alive := m.processStatus(ProtectedService); !alive {
		t.Fatal("protected service was signalled despite missing attribution")
	}
	_ = pid
}

// TestProtectedStopWithMaintenance verifies an attributed, bounded stop
// records a lease before the signal and the service stops cleanly.
func TestProtectedStopWithMaintenance(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess(ProtectedService, "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := m.StartProcess(ProtectedService); err != nil {
		t.Fatalf("start: %v", err)
	}
	req := StopRequest{Actor: "tester", Reason: "planned maintenance", For: time.Hour, Command: "stop"}
	if err := m.StopProcessMaintenance(ProtectedService, req); err != nil {
		t.Fatalf("maintenance stop: %v", err)
	}
	data := m.loadStateFile()
	p := data.Processes[ProtectedService]
	if p.Maintenance == nil {
		t.Fatal("expected maintenance lease recorded before stop")
	}
	if p.Maintenance.Actor != "tester" || p.Maintenance.Reason != "planned maintenance" {
		t.Fatalf("unexpected lease: %+v", p.Maintenance)
	}
	if !p.ExplicitlyStopped {
		t.Fatal("expected explicit stop flag")
	}
}

// TestProtectedStartClearsMaintenance verifies an ordinary start resolves an
// expired maintenance lease and records lifecycle attribution.
func TestProtectedStartClearsMaintenance(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess(ProtectedService, "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	expired := Maintenance{Actor: "a", Reason: "r", Command: "stop", Until: nowUnix() - 10, StoppedAt: nowUnix() - 100}
	m.mutateProcess(ProtectedService, func(p *Process) {
		p.Maintenance = &expired
		p.ExplicitlyStopped = true
	})
	pid, err := m.StartProcess(ProtectedService)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = m.StopProcess(ProtectedService, false) }()
	data := m.loadStateFile()
	p := data.Processes[ProtectedService]
	if p.Maintenance != nil {
		t.Fatal("expected maintenance lease cleared by start")
	}
	if p.LastLifecycleActor == "" || p.LastLifecycleCommand != "start" {
		t.Fatalf("expected lifecycle attribution, got actor=%q command=%q", p.LastLifecycleActor, p.LastLifecycleCommand)
	}
	_ = pid
}

// TestProtectedOutages verifies the watcher reports a protected service still
// stopped after its lease expired, and writes a bounded outage marker.
func TestProtectedOutages(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess(ProtectedService, "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	expired := Maintenance{Actor: "a", Reason: "r", Command: "stop", Until: nowUnix() - 10, StoppedAt: nowUnix() - 100}
	m.mutateProcess(ProtectedService, func(p *Process) {
		p.Maintenance = &expired
		p.ExplicitlyStopped = true
	})
	outages := m.ProtectedOutages()
	if len(outages) != 1 {
		t.Fatalf("expected one outage, got %d", len(outages))
	}
	m.reportProtectedOutages()
	marker := filepath.Join(m.Root(), "output", "outages", ProtectedService+".json")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected outage marker at %s: %v", marker, err)
	}
}

// TestProtectedOutagesSkipsLiveLease verifies an active lease is not an outage.
func TestProtectedOutagesSkipsLiveLease(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess(ProtectedService, "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	live := Maintenance{Actor: "a", Reason: "r", Command: "stop", Until: nowUnix() + 3600, StoppedAt: nowUnix()}
	m.mutateProcess(ProtectedService, func(p *Process) {
		p.Maintenance = &live
		p.ExplicitlyStopped = true
	})
	if outages := m.ProtectedOutages(); len(outages) != 0 {
		t.Fatalf("expected no outage with live lease, got %d", len(outages))
	}
}

// TestUnprotectedServiceUnchanged verifies non-protected services keep the
// legacy explicit-stop semantics with no attribution requirement.
func TestUnprotectedServiceUnchanged(t *testing.T) {
	m := newTestManager(t)
	if err := m.AddProcess("other", "sleep 60", nil, ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := m.StartProcess("other"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.StopProcess("other", true); err != nil {
		t.Fatalf("unprotected stop should not require attribution: %v", err)
	}
	data := m.loadStateFile()
	if data.Processes["other"].Maintenance != nil {
		t.Fatal("unprotected service should never gain a maintenance lease")
	}
}

// TestMaintenanceDurationBounds verifies the bounded window is enforced.
func TestMaintenanceDurationBounds(t *testing.T) {
	if _, err := ParseMaintenanceDuration("30s"); err == nil {
		t.Fatal("expected sub-minute duration rejected")
	}
	if _, err := ParseMaintenanceDuration("3h"); err == nil {
		t.Fatal("expected over-2h duration rejected")
	}
	d, err := ParseMaintenanceDuration("30m")
	if err != nil || d != 30*time.Minute {
		t.Fatalf("expected 30m accepted, got %v %v", d, err)
	}
}

// TestStopRequestValidation verifies attribution fields are bounded and required.
func TestStopRequestValidation(t *testing.T) {
	base := StopRequest{Actor: "a", Reason: "r", For: time.Hour, Command: "stop"}
	if err := base.validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := []StopRequest{
		{Actor: "", Reason: "r", For: time.Hour, Command: "stop"},
		{Actor: "a", Reason: "", For: time.Hour, Command: "stop"},
		{Actor: "a", Reason: "r", For: time.Hour, Command: ""},
		{Actor: "a", Reason: "r", For: 10 * time.Second, Command: "stop"},
		{Actor: "a", Reason: "r", For: 3 * time.Hour, Command: "stop"},
		{Actor: strings.Repeat("x", 200), Reason: "r", For: time.Hour, Command: "stop"},
	}
	for i, c := range cases {
		if err := c.validate(); err == nil {
			t.Fatalf("case %d should be rejected", i)
		}
	}
}
