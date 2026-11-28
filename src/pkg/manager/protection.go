package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProtectedService is the one unified AgentD3 host. Protection is deliberately
// exact: unrelated services retain their existing stop semantics.
const ProtectedService = "daz"

const (
	MinMaintenanceDuration = time.Minute
	MaxMaintenanceDuration = 2 * time.Hour
	maxAttributionReason   = 512
	maxAttributionActor    = 128
)

// StopRequest describes one attributed, bounded explicit stop. Actor and reason
// are public bookkeeping, never credentials or arbitrary command input.
type StopRequest struct {
	Actor   string
	Reason  string
	For     time.Duration
	Command string
}

// Maintenance is the durable lease created before a protected service is
// signalled. It is omitted from state when no lease is active.
type Maintenance struct {
	Actor     string  `json:"actor"`
	Reason    string  `json:"reason"`
	Command   string  `json:"command"`
	Until     float64 `json:"until"`
	StoppedAt float64 `json:"stopped_at"`
}

// IsProtected reports whether name is the one protected unified host. It is
// exported for CLI gating; all enforcement lives in the manager.
func IsProtected(name string) bool { return isProtected(name) }

func isProtected(name string) bool { return name == ProtectedService }

// ParseMaintenanceDuration accepts Auto's existing duration syntax and applies
// the narrow maintenance bounds. Fractions are permitted only by ParseInterval.
func ParseMaintenanceDuration(value string) (time.Duration, error) {
	seconds, err := ParseInterval(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	d := time.Duration(seconds) * time.Second
	if d < MinMaintenanceDuration || d > MaxMaintenanceDuration {
		return 0, fmt.Errorf("maintenance duration must be between 1m and 2h")
	}
	return d, nil
}

func (r StopRequest) validate() error {
	if strings.TrimSpace(r.Actor) == "" || len(r.Actor) > maxAttributionActor {
		return fmt.Errorf("stop attribution requires an actor of at most %d bytes", maxAttributionActor)
	}
	if strings.TrimSpace(r.Reason) == "" || len(r.Reason) > maxAttributionReason {
		return fmt.Errorf("stop attribution requires a reason of at most %d bytes", maxAttributionReason)
	}
	if strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("stop attribution requires a command")
	}
	if r.For < MinMaintenanceDuration || r.For > MaxMaintenanceDuration {
		return fmt.Errorf("maintenance duration must be between 1m and 2h")
	}
	return nil
}

// BeginMaintenance atomically validates and records a lease before any stop
// signal. A second writer cannot replace a live lease without seeing it.
func (m *Manager) BeginMaintenance(name string, request StopRequest) (Maintenance, error) {
	if !isProtected(name) {
		return Maintenance{}, fmt.Errorf("process %s is not protected", name)
	}
	if err := request.validate(); err != nil {
		return Maintenance{}, err
	}
	lease := Maintenance{
		Actor: request.Actor, Reason: request.Reason, Command: request.Command,
		Until: nowUnix() + request.For.Seconds(), StoppedAt: nowUnix(),
	}
	committed := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.Command == "" {
			return false
		}
		if maintenanceLive(p.Maintenance, nowUnix()) {
			return false
		}
		p.Maintenance = &lease
		p.ExplicitlyStopped = true
		committed = true
		return true
	})
	if !committed {
		if _, ok := m.definition(name); !ok {
			return Maintenance{}, fmt.Errorf("process %s not found in config", name)
		}
		return Maintenance{}, fmt.Errorf("process %s already has an active maintenance lease", name)
	}
	return lease, nil
}

func maintenanceLive(lease *Maintenance, now float64) bool {
	return lease != nil && lease.Until > now
}

// StopProcessMaintenance performs an explicit stop only with a durable,
// bounded lease. StopProcess remains the internal primitive used by restart,
// periodic maintenance, handover and shutdown.
func (m *Manager) StopProcessMaintenance(name string, request StopRequest) error {
	if !isProtected(name) {
		return m.StopProcess(name, true)
	}
	if _, err := m.BeginMaintenance(name, request); err != nil {
		return err
	}
	return m.StopProcess(name, true)
}

// RemoveProcessMaintenance protects the destructive bulk/API equivalent too.
func (m *Manager) RemoveProcessMaintenance(name string, request StopRequest) error {
	if !isProtected(name) {
		return m.RemoveProcess(name)
	}
	if _, err := m.BeginMaintenance(name, request); err != nil {
		return err
	}
	return m.RemoveProcess(name)
}

// ProtectedOutages returns leases whose service is still durably stopped after
// expiry. It is consumed by Auto's watcher, not by the protected suite.
func (m *Manager) ProtectedOutages() []Maintenance {
	data := m.loadStateFile()
	var outages []Maintenance
	for name, p := range data.Processes {
		if !isProtected(name) || maintenanceLive(p.Maintenance, nowUnix()) {
			continue
		}
		if p.Maintenance != nil && !p.ExplicitlyStopped {
			continue
		}
		if _, alive := m.processStatus(name); alive {
			continue
		}
		if p.Maintenance != nil {
			outages = append(outages, *p.Maintenance)
		}
	}
	return outages
}

// RecordLifecycleAttribution stores only bounded, non-secret lifecycle metadata
// on the protected entry. Start clears an expired maintenance lease; an active
// one is an explicit operator resolution and is cleared by the ordinary start.
func (m *Manager) RecordLifecycleAttribution(name, actor, command string) {
	if !isProtected(name) || strings.TrimSpace(actor) == "" || len(actor) > maxAttributionActor ||
		strings.TrimSpace(command) == "" || len(command) > maxAttributionReason {
		return
	}
	m.mutateProcess(name, func(p *Process) {
		p.LastLifecycleActor = actor
		p.LastLifecycleCommand = command
		p.LastLifecycleAt = float64Ptr(nowUnix())
		if !maintenanceLive(p.Maintenance, nowUnix()) {
			p.Maintenance = nil
		}
	})
}

func (m *Manager) writeOutageMarker(lease Maintenance) error {
	dir := filepath.Join(m.root, "output", "outages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ProtectedService+".json")
	tmp, err := os.CreateTemp(dir, ".outage-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(payload, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// reportProtectedOutages runs from the watch daemon's tick. It writes a
// bounded, non-secret outage marker when the protected service remains stopped
// after its maintenance lease expired. The marker is outside the protected
// service's own state, so external monitors can read it even if the suite is
// down.
func (m *Manager) reportProtectedOutages() {
	for _, lease := range m.ProtectedOutages() {
		if err := m.writeOutageMarker(lease); err != nil {
			fmt.Printf("Outage marker write failed: %v\n", err)
			continue
		}
		fmt.Printf("PROTECTED OUTAGE: %s stopped by %s until %.0f (reason: %s)\n",
			ProtectedService, lease.Actor, lease.Until, lease.Reason)
	}
}

func float64Ptr(v float64) *float64 { return &v }
