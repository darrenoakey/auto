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
		return withArgs(args, valueSet("port", "restart-every"), nil, func(p *parsedArgs) int { return cmdAdd(m, p) })
	case "update":
		return withArgs(args, valueSet("command", "port", "workdir", "restart-every"), nil, func(p *parsedArgs) int { return cmdUpdate(m, p) })
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
func dispatchNamed(m *manager.Manager, command string, args []string) int {
	return withArgs(args, nil, nil, func(p *parsedArgs) int {
		name, err := p.requireName()
		if err != nil {
			return failf("%v", err)
		}
		switch command {
		case "start":
			return cmdStart(m, name)
		case "stop":
			return cmdStop(m, name)
		case "restart":
			return cmdRestart(m, name)
		case "remove":
			return cmdRemove(m, name)
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
	fmt.Println("Commands: ps start stop restart add update remove show status log start-all stop-all restart-all watch shutdown install")
}
