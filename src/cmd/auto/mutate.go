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
	if err := m.AddProcess(name, command, port, ""); err != nil {
		return failf("%v", err)
	}
	if code := applyRestartEvery(m, name, p); code != 0 {
		return code
	}
	fmt.Printf("Added %s\n", name)
	return cmdStart(m, name)
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
