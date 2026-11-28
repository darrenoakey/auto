// Command auto is a macOS daemon process manager. It stores process definitions
// and runtime state in a single state.json file and supervises them with restart
// backoff. Built and signed as Auto.app so the watch daemon is its own
// responsible process for macOS Local Network authorization.
package main

import (
	"fmt"
	"os"

	"auto/pkg/manager"
)

// main dispatches the CLI and exits with the handler's status code.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run parses global flags and dispatches the subcommand.
func run(args []string) int {
	rest, quiet, banner := extractGlobalFlags(args)
	if len(rest) == 0 {
		usage()
		return 2
	}
	maybeShowBanner(quiet, banner)
	m := manager.Default()
	return dispatch(m, rest[0], rest[1:])
}

// dispatch routes a subcommand to its handler.
func dispatch(m *manager.Manager, command string, args []string) int {
	switch command {
	case "ps":
		return cmdPs(m)
	case "start", "stop", "restart", "remove", "show", "status":
		return dispatchNamed(m, command, args)
	case "add":
		return withArgs(args, valueSet("port", "workdir", "restart-every", "isolate"), nil, func(p *parsedArgs) int { return cmdAdd(m, p) })
	case "update":
		return withArgs(args, valueSet("command", "port", "workdir", "restart-every", "isolate"), nil, func(p *parsedArgs) int { return cmdUpdate(m, p) })
	case "log":
		return withArgs(args, nil, boolSet("file", "tail"), func(p *parsedArgs) int { return cmdLog(m, p) })
	case "start-all":
		return cmdStartAll(m)
	case "stop-all":
		return cmdStopAll(m)
	case "restart-all":
		return cmdRestartAll(m)
	case "watch":
		return runWatch(m)
	case "shutdown":
		return cmdShutdown(m)
	case "install":
		return cmdInstall(m)
	default:
		usage()
		return 2
	}
}

// dispatchNamed handles the single-positional commands that take just a name.
// restart additionally accepts one overlap mode.
func dispatchNamed(m *manager.Manager, command string, args []string) int {
	boolFlags := map[string]bool{}
	valueFlags := map[string]bool{}
	if command == "restart" {
		boolFlags = boolSet("overlap", "strict-overlap", "term-only")
	}
	if command == "stop" || command == "remove" {
		valueFlags = attributionFlags()
	}
	return withArgs(args, valueFlags, boolFlags, func(p *parsedArgs) int {
		name, err := p.requireName()
		if err != nil {
			return failf("%v", err)
		}
		switch command {
		case "start":
			return cmdStart(m, name)
		case "stop":
			return cmdStop(m, name, p)
		case "restart":
			if countModes(p) > 1 {
				return failf("--overlap, --strict-overlap and --term-only are mutually exclusive")
			}
			return cmdRestart(m, name, p.bools["overlap"], p.bools["strict-overlap"], p.bools["term-only"])
		case "remove":
			return cmdRemove(m, name, p)
		default: // show, status
			return cmdShow(m, name)
		}
	})
}

// withArgs parses a subcommand's arguments and invokes the handler.
func withArgs(args []string, valueFlags, boolFlags map[string]bool, handler func(*parsedArgs) int) int {
	p, err := parseArgs(args, valueFlags, boolFlags)
	if err != nil {
		return failf("%v", err)
	}
	return handler(p)
}

// extractGlobalFlags removes -q/--quiet and --banner from anywhere in the args,
// returning the remaining args and the flag states.
func extractGlobalFlags(args []string) (rest []string, quiet, banner bool) {
	for _, arg := range args {
		switch arg {
		case "-q", "--quiet":
			quiet = true
		case "--banner":
			banner = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, quiet, banner
}

// valueSet builds a set of recognised value-flag names.
func valueSet(names ...string) map[string]bool {
	return toSet(names)
}

// boolSet builds a set of recognised bool-flag names.
func boolSet(names ...string) map[string]bool {
	return toSet(names)
}

// toSet turns a name list into a set.
func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// usage prints the command summary.
func usage() {
	fmt.Println("Usage: auto [-q|--banner] <command> [args]")
	fmt.Println("Commands: ps start stop restart [--overlap|--strict-overlap|--term-only] add update remove show status log start-all stop-all restart-all watch shutdown install")
}

// countModes counts how many restart overlap/termination mode flags are set.
func countModes(p *parsedArgs) int {
	count := 0
	for _, mode := range []string{"overlap", "strict-overlap", "term-only"} {
		if p.bools[mode] {
			count++
		}
	}
	return count
}

// attributionFlags returns the bounded attribution value flags for stop and remove.
func attributionFlags() map[string]bool {
	return valueSet("actor", "reason", "for")
}

// maintenanceRequest builds a bounded StopRequest from parsed attribution flags.
func maintenanceRequest(p *parsedArgs, command string) (manager.StopRequest, error) {
	actor, _ := p.values["actor"]
	reason, _ := p.values["reason"]
	forValue, _ := p.values["for"]
	if actor == "" || reason == "" || forValue == "" {
		return manager.StopRequest{}, fmt.Errorf("protected stop requires --actor, --reason, and --for (1m-2h)")
	}
	d, err := manager.ParseMaintenanceDuration(forValue)
	if err != nil {
		return manager.StopRequest{}, err
	}
	return manager.StopRequest{Actor: actor, Reason: reason, For: d, Command: command}, nil
}
