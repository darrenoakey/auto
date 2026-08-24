// Command auto-shim execs a shell command in place, replacing its own process
// image. It exists so isolate-mode LaunchAgents can run interpreter-based
// commands (Python, etc.) without hanging.
//
// On this machine (macOS 26 Tahoe, beta), a Python (including PyInstaller-
// bundled Mach-O) process launched as the TOP-LEVEL executable image of a
// freshly bootstrapped LaunchAgent job hangs indefinitely during interpreter
// initialization — reproduced twice, stuck inside Py_InitializeFromConfig,
// 0% CPU, zero file I/O, for minutes. The identical binary launched by an
// already-running warm process (e.g. auto's own watch daemon, or a shell)
// starts normally in under a second. A compiled Go top-level image never
// hits the stall, and exec() never changes a task's pid, XNU resource
// coalition, or launchd job identity (all fixed at spawn time by launchd) —
// only the running image. So: launchd spawns this tiny Go binary (no stall),
// which immediately execs the real command from inside an already-running
// process (no stall), landing in the exact coalition launchd assigned at
// spawn. See ~/local/auto/README.md, "Windowed GUI apps showing wildly
// inflated / identical memory in Force Quit".
package main

import (
	"fmt"
	"os"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: auto-shim <command>")
		os.Exit(2)
	}
	command := os.Args[1]
	argv := []string{"sh", "-c", "exec " + command}
	if err := syscall.Exec("/bin/sh", argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "auto-shim: exec failed: %v\n", err)
		os.Exit(1)
	}
}
