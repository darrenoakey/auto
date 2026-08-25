package main

import (
	"fmt"
	"strconv"
	"strings"

	"auto/pkg/manager"
)

// cmdAdd registers a new process and starts it immediately.
func cmdAdd(m *manager.Manager, p *parsedArgs) int {
	if len(p.positional) < 2 {
		return failf("add requires a name and a command")
	}
	name := p.positional[0]
	command := strings.Join(p.positional[1:], " ")
	port, err := optPort(p)
	if err != nil {
		return failf("%v", err)
	}
	workdir := workdirArg(p)
	if err := m.AddProcess(name, command, port, workdir); err != nil {
		return failf("%v", err)
	}
	warnRelativeCommand(command, workdir)
	if code := applyIsolate(m, name, p); code != 0 {
		return code
	}
	if code := applyRestartEvery(m, name, p); code != 0 {
		return code
	}
	fmt.Printf("Added %s (workdir: %s)\n", name, effectiveWorkdir(workdir))
	return cmdStart(m, name)
}

// workdirArg returns the workdir= value flag, or "" when absent.
func workdirArg(p *parsedArgs) string {
	if v := optStr(p, "workdir"); v != nil {
		return *v
	}
	return ""
}

// effectiveWorkdir renders a stored workdir for add output: unset means the
// service runs from the filesystem root.
func effectiveWorkdir(workdir string) string {
	if workdir == "" {
		return "/"
	}
	return workdir
}

// warnRelativeCommand warns when a command that depends on its working
// directory was added without one: it will run from / and fail loudly.
func warnRelativeCommand(command, workdir string) {
	if !commandNeedsWorkdir(command, workdir) {
		return
	}
	fields := strings.Fields(command)
	fmt.Printf("warning: %q is relative but no workdir is set; it will run from / — pass workdir=<dir>\n", fields[0])
}

// commandNeedsWorkdir reports whether the command starts with a relative
// path while no workdir is set, so it cannot resolve from the spawn root.
func commandNeedsWorkdir(command, workdir string) bool {
	if workdir != "" {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	return strings.HasPrefix(fields[0], "./") || strings.HasPrefix(fields[0], "../")
}

// cmdUpdate updates settings for an existing process.
func cmdUpdate(m *manager.Manager, p *parsedArgs) int {
	name, err := p.requireName()
	if err != nil {
		return failf("%v", err)
	}
	port, perr := optPort(p)
	if perr != nil {
		return failf("%v", perr)
	}
	if err := m.UpdateProcess(name, optStr(p, "command"), port, optStr(p, "workdir")); err != nil {
		return failf("%v", err)
	}
	if code := applyIsolate(m, name, p); code != 0 {
		return code
	}
	return reportUpdate(m, name, p)
}

// reportUpdate applies an optional --restart-every change and prints the result.
func reportUpdate(m *manager.Manager, name string, p *parsedArgs) int {
	re, ok := p.values["restart-every"]
	if !ok {
		fmt.Printf("Updated %s\n", name)
		return 0
	}
	if strings.EqualFold(re, "off") {
		if err := m.SetRestartInterval(name, nil); err != nil {
			return failf("%v", err)
		}
		fmt.Printf("Updated %s (periodic restart disabled)\n", name)
		return 0
	}
	secs, err := manager.ParseInterval(re)
	if err != nil {
		return failf("%v", err)
	}
	if err := m.SetRestartInterval(name, &secs); err != nil {
		return failf("%v", err)
	}
	fmt.Printf("Updated %s (periodic restart every %s)\n", name, manager.FormatInterval(secs))
	return 0
}

// applyRestartEvery sets a periodic restart interval from --restart-every if given.
func applyRestartEvery(m *manager.Manager, name string, p *parsedArgs) int {
	re, ok := p.values["restart-every"]
	if !ok {
		return 0
	}
	secs, err := manager.ParseInterval(re)
	if err != nil {
		return failf("%v", err)
	}
	if err := m.SetRestartInterval(name, &secs); err != nil {
		return failf("%v", err)
	}
	return 0
}

// applyIsolate sets or clears isolate mode from --isolate=on|off if given.
func applyIsolate(m *manager.Manager, name string, p *parsedArgs) int {
	v, ok := p.values["isolate"]
	if !ok {
		return 0
	}
	var isolate bool
	switch strings.ToLower(v) {
	case "on", "true", "yes":
		isolate = true
	case "off", "false", "no":
		isolate = false
	default:
		return failf("--isolate must be on or off, got %q", v)
	}
	if err := m.SetIsolate(name, isolate); err != nil {
		return failf("%v", err)
	}
	return 0
}

// optPort returns the --port flag as an *int, or nil if absent.
func optPort(p *parsedArgs) (*int, error) {
	v, ok := p.values["port"]
	if !ok {
		return nil, nil
	}
	port, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("port must be an integer")
	}
	return &port, nil
}

// optStr returns a value flag as a *string, or nil if absent.
func optStr(p *parsedArgs, name string) *string {
	if v, ok := p.values[name]; ok {
		return &v
	}
	return nil
}
