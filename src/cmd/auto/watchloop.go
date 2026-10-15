package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"auto/pkg/manager"
)

// runWatch supervises until interrupted. Kernel-confirmed shutdown is handled
// without exiting into launchd KeepAlive; SIGTERM also cleanly stops on bootout.
func runWatch(m *manager.Manager) int {
	disableAppNap()
	sigCh, stopSignals := watchSignals(m)
	defer stopSignals()
	fmt.Println("Watching processes for automatic restart...")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	shutdownComplete := checkSystemShutdown(m, false)
	for {
		select {
		case sig := <-sigCh:
			if shutdownComplete {
				return 0
			}
			return handleWatchSignal(m, sig)
		case <-ticker.C:
			shutdownComplete = checkSystemShutdown(m, shutdownComplete)
		}
	}
}

// watchSignals inhibits launches immediately, even while the main goroutine is
// inside a slow spawn/retry/tick. The main goroutine then drains and tears down.
func watchSignals(m *manager.Manager) (<-chan os.Signal, func()) {
	raw, received := make(chan os.Signal, 1), make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(raw, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-raw:
			m.InhibitStarts()
			received <- sig
		case <-done:
		}
	}()
	return received, func() { signal.Stop(raw); close(done) }
}

// checkSystemShutdown also runs before the first tick. A new watcher during
// this boot sees the same kernel flag; it never clears shutdown on startup.
// A failed read blocks supervision without falsely declaring machine shutdown.
func checkSystemShutdown(m *manager.Manager, complete bool) bool {
	if complete {
		return true
	}
	shutdown, err := manager.SystemShutdownRequested()
	if err != nil {
		fmt.Printf("Shutdown state unavailable; supervision paused: %v\n", err)
		return false
	}
	if !shutdown {
		runTickSafely(m)
		return false
	}
	m.InhibitStarts()
	fmt.Println("Machine shutdown confirmed by kern.willshutdown; stopping all services...")
	m.ShutdownAll()
	fmt.Println("Shutdown complete; starts inhibited until this watcher exits")
	return true
}

// handleWatchSignal does not call SIGTERM a machine shutdown: manual bootout
// sends it too. SIGINT leaves existing services running, as before.
func handleWatchSignal(m *manager.Manager, sig os.Signal) int {
	m.InhibitStarts()
	if sig == syscall.SIGINT {
		fmt.Println("\nStopped watching")
		return 0
	}
	fmt.Println("\nSIGTERM received, stopping all managed processes...")
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
