package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"auto/pkg/manager"
)

// runWatch runs the supervision loop until interrupted. SIGTERM (sent during
// system shutdown) triggers a clean teardown of all managed processes; SIGINT
// just stops watching. A single bad tick never kills the supervisor.
func runWatch(m *manager.Manager) int {
	disableAppNap()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	fmt.Println("Watching processes for automatic restart...")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case sig := <-sigCh:
			return handleWatchSignal(m, sig)
		case <-ticker.C:
			runTickSafely(m)
		}
	}
}

// handleWatchSignal performs the appropriate teardown for a received signal.
func handleWatchSignal(m *manager.Manager, sig os.Signal) int {
	if sig == syscall.SIGINT {
		fmt.Println("\nStopped watching")
		return 0
	}
	fmt.Println("\nSIGTERM received, shutting down all processes...")
	m.ShutdownAll()
	fmt.Println("Shutdown complete")
	return 0
}

// runTickSafely runs one watch tick, recovering from any panic so launchd does
// not throttle-unload the agent and leave nothing supervised.
func runTickSafely(m *manager.Manager) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Watch tick error (continuing): %v\n", r)
		}
	}()
	m.WatchTick()
}
