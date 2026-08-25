package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProcessInfo is a snapshot of a process for display by `ps`.
type ProcessInfo struct {
	Name              string
	Command           string
	Pid               int // 0 when not running
	Running           bool
	Port              *int
	Workdir           string
	ExplicitlyStopped bool
	RestartInterval   *int
	Isolate           bool
}

// AddProcess registers a new process definition. A supplied workdir must
// exist and is stored absolute; an unset workdir is stored empty, meaning the
// service runs from the filesystem root at spawn time — never the working
// directory of whatever process invoked auto, which once silently mislabeled
// services in monitors that attribute processes by cwd.
func (m *Manager) AddProcess(name, command string, port *int, workdir string) error {
	if err := validatePort(port); err != nil {
		return err
	}
	if workdir != "" {
		resolved, err := resolveExistingDir(workdir)
		if err != nil {
			return err
		}
		workdir = resolved
	}
	duplicate := false
	m.withState(func(data *stateFile) bool {
		if _, exists := data.Processes[name]; exists {
			duplicate = true
			return false
		}
		data.Processes[name] = &Process{Command: command, Port: port, Workdir: workdir}
		return true
	})
	if duplicate {
		return fmt.Errorf("process %s already exists", name)
	}
	return nil
}

// UpdateProcess updates the command, port, or workdir of an existing process.
// Nil arguments leave the corresponding field unchanged. A non-nil empty
// workdir clears the field (the service runs from the filesystem root);
// filepath.Abs("") would otherwise resolve to the updater's own cwd — the
// same silent capture add used to have.
func (m *Manager) UpdateProcess(name string, command *string, port *int, workdir *string) error {
	if err := validatePort(port); err != nil {
		return err
	}
	resolvedWd := ""
	if workdir != nil && *workdir != "" {
		wd, err := resolveExistingDir(*workdir)
		if err != nil {
			return err
		}
		resolvedWd = wd
	}
	found := false
	m.withState(func(data *stateFile) bool {
		p, ok := data.Processes[name]
		if !ok || p.Command == "" {
			return false
		}
		found = true
		if command != nil {
			p.Command = *command
		}
		if port != nil {
			p.Port = port
		}
		if workdir != nil {
			p.Workdir = resolvedWd
		}
		return true
	})
	if !found {
		return fmt.Errorf("process %s not found in config", name)
	}
	return nil
}

// RemoveProcess stops a process if running and deletes it from the state file.
func (m *Manager) RemoveProcess(name string) error {
	def, ok := m.definition(name)
	if !ok {
		return fmt.Errorf("process %s not found in config", name)
	}
	if def.Isolate {
		if err := m.isolateRemoveArtifacts(name); err != nil {
			return err
		}
	} else if _, alive := m.processStatus(name); alive {
		if err := m.StopProcess(name, true); err != nil {
			return err
		}
	}
	m.withState(func(data *stateFile) bool {
		if _, ok := data.Processes[name]; !ok {
			return false
		}
		delete(data.Processes, name)
		return true
	})
	return nil
}

// definedNames returns the sorted names of processes that have a command — i.e.
// real definitions — skipping runtime-only stub entries. This mirrors the
// original load_config, which silently ignored entries without a command.
func (m *Manager) definedNames() []string {
	data := m.loadStateFile()
	names := make([]string, 0, len(data.Processes))
	for name, p := range data.Processes {
		if p.Command != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ListProcesses returns display snapshots for all configured processes.
func (m *Manager) ListProcesses() []ProcessInfo {
	data := m.loadStateFile()
	infos := make([]ProcessInfo, 0, len(data.Processes))
	for _, name := range m.definedNames() {
		p := data.Processes[name]
		pid, running := m.processStatus(name)
		infos = append(infos, ProcessInfo{
			Name:              name,
			Command:           p.Command,
			Pid:               pid,
			Running:           running,
			Port:              p.Port,
			Workdir:           p.Workdir,
			ExplicitlyStopped: p.ExplicitlyStopped,
			RestartInterval:   p.RestartIntervalSeconds,
			Isolate:           p.Isolate,
		})
	}
	return infos
}

// GetCommand returns the command line of a configured process.
func (m *Manager) GetCommand(name string) (string, error) {
	def, ok := m.definition(name)
	if !ok {
		return "", fmt.Errorf("process %s not found in config", name)
	}
	return def.Command, nil
}

// GetWorkdir returns the configured working directory of a process, or an
// empty string when unset (the service runs from the filesystem root).
func (m *Manager) GetWorkdir(name string) (string, error) {
	def, ok := m.definition(name)
	if !ok {
		return "", fmt.Errorf("process %s not found in config", name)
	}
	return def.Workdir, nil
}

// LatestLogPath returns the most recent log file for a process, or "" if none.
func (m *Manager) LatestLogPath(name string) string {
	return m.latestLogPath(name)
}

// validatePort rejects out-of-range ports.
func validatePort(port *int) error {
	if port == nil {
		return nil
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

// resolveExistingDir expands, absolutises, and verifies a directory path.
func resolveExistingDir(workdir string) (string, error) {
	expanded := expandHome(workdir)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workdir does not exist: %s", abs)
	}
	return abs, nil
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
