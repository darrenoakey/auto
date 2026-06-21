package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"

	"auto/pkg/install"
	"auto/pkg/manager"
)

// cmdPs prints all configured processes with their current status.
func cmdPs(m *manager.Manager) int {
	infos := m.ListProcesses()
	if len(infos) == 0 {
		fmt.Println("No processes configured")
		return 0
	}
	nameWidth := len("NAME")
	for _, info := range infos {
		if len(info.Name) > nameWidth {
			nameWidth = len(info.Name)
		}
	}
	fmt.Printf("%-*s  %6s  %5s  %9s\n", nameWidth, "NAME", "PID", "PORT", "RESTART")
	for _, info := range infos {
		fmt.Printf("%-*s  %6s  %5s  %9s\n", nameWidth, info.Name, pidColumn(info), portColumn(info), restartColumn(info))
	}
	return 0
}

// pidColumn renders the PID column: the live pid, "stopped", or "dead".
func pidColumn(info manager.ProcessInfo) string {
	switch {
	case info.Running:
		return strconv.Itoa(info.Pid)
	case info.ExplicitlyStopped:
		return "stopped"
	default:
		return "dead"
	}
}

// portColumn renders the PORT column.
func portColumn(info manager.ProcessInfo) string {
	if info.Port == nil {
		return "-"
	}
	return strconv.Itoa(*info.Port)
}

// restartColumn renders the periodic-restart interval column.
func restartColumn(info manager.ProcessInfo) string {
	if info.RestartInterval == nil {
		return "-"
	}
	return manager.FormatInterval(*info.RestartInterval)
}

// cmdStart starts a configured process.
func cmdStart(m *manager.Manager, name string) int {
	pid, err := m.StartProcess(name)
	if err != nil {
		return failf("%v", err)
	}
	fmt.Printf("Started %s with pid %d\n", name, pid)
	return 0
}

// cmdStop stops a running process.
func cmdStop(m *manager.Manager, name string) int {
	if err := m.StopProcess(name, true); err != nil {
		return failf("%v", err)
	}
	fmt.Printf("Stopped %s\n", name)
	return 0
}

// cmdRestart restarts a process.
func cmdRestart(m *manager.Manager, name string) int {
	pid, err := m.RestartProcess(name)
	if err != nil {
		return failf("%v", err)
	}
	fmt.Printf("Restarted %s with pid %d\n", name, pid)
	return 0
}

// cmdRemove removes a process from config.
func cmdRemove(m *manager.Manager, name string) int {
	if err := m.RemoveProcess(name); err != nil {
		return failf("%v", err)
	}
	fmt.Printf("Removed %s\n", name)
	return 0
}

// cmdShow displays a process command and any periodic restart interval.
func cmdShow(m *manager.Manager, name string) int {
	command, err := m.GetCommand(name)
	if err != nil {
		return failf("%v", err)
	}
	fmt.Printf("%s: %s\n", name, command)
	if iv := m.GetRestartInterval(name); iv != nil {
		fmt.Printf("  restart every: %s\n", manager.FormatInterval(*iv))
	}
	return 0
}

// cmdLog shows, locates, or tails the latest log file for a process.
func cmdLog(m *manager.Manager, p *parsedArgs) int {
	name, err := p.requireName()
	if err != nil {
		return failf("%v", err)
	}
	logPath := m.LatestLogPath(name)
	if logPath == "" {
		return failf("No log file found for %s", name)
	}
	if p.bools["file"] {
		fmt.Println(logPath)
		return 0
	}
	if p.bools["tail"] {
		c := exec.Command("tail", "-f", logPath)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		_ = c.Run()
		return 0
	}
	return printFile(logPath)
}

// printFile writes a file's contents to stdout.
func printFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return failf("%v", err)
	}
	fmt.Print(string(data))
	return 0
}

// cmdStartAll starts every configured process not already running.
func cmdStartAll(m *manager.Manager) int {
	m.StartAll()
	fmt.Println("Started all configured processes")
	return 0
}

// cmdStopAll stops every running managed process without marking them explicitly
// stopped, so the watch daemon respawns them (under its own signed identity).
func cmdStopAll(m *manager.Manager) int {
	var running []string
	for _, info := range m.ListProcesses() {
		if info.Running {
			running = append(running, info.Name)
		}
	}
	m.ShutdownAll()
	fmt.Printf("Stopped %d running process(es); the watch daemon will respawn them\n", len(running))
	return 0
}

// cmdRestartAll restarts all dead, non-explicitly-stopped processes.
func cmdRestartAll(m *manager.Manager) int {
	results := m.RestartDead()
	if len(results) == 0 {
		fmt.Println("No dead processes to restart")
		return 0
	}
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	failed := false
	for _, name := range names {
		failed = reportRestart(name, results[name]) || failed
	}
	if failed {
		return 1
	}
	return 0
}

// reportRestart prints one restart-all result and reports whether it failed.
func reportRestart(name, result string) bool {
	if len(result) > 4 && result[:4] == "pid " {
		fmt.Printf("Restarted %s with %s\n", name, result)
		return false
	}
	fmt.Printf("Failed to restart %s: %s\n", name, result)
	return true
}

// cmdShutdown stops all managed processes and the daemon for reboot.
func cmdShutdown(m *manager.Manager) int {
	var running []string
	for _, info := range m.ListProcesses() {
		if info.Running {
			running = append(running, info.Name)
		}
	}
	if len(running) > 0 {
		fmt.Printf("Stopping %d managed process(es): %v\n", len(running), running)
	}
	m.ShutdownForReboot()
	fmt.Println("Shutdown complete. Auto daemon stopped. Safe to reboot.")
	return 0
}

// cmdInstall installs the signed daemon and wrapper.
func cmdInstall(m *manager.Manager) int {
	if err := install.Install(m.Root()); err != nil {
		return failf("%v", err)
	}
	return 0
}

// failf prints an error and returns exit code 1.
func failf(format string, args ...any) int {
	fmt.Printf("Error: "+format+"\n", args...)
	return 1
}
