//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation

#import <Foundation/Foundation.h>

// Hold a single process-wide activity assertion for the lifetime of the daemon.
static id autoActivityToken = nil;

// beginSupervisorActivity opts the watch daemon out of App Nap. A background,
// occluded daemon would otherwise have its timers, CPU, and network I/O
// throttled — the supervision loop would stop firing and managed processes would
// drift to dead without being restarted. NSActivityUserInitiatedAllowingIdle-
// SystemSleep marks the work as ongoing and user-relevant while still allowing
// normal idle system sleep. The token is retained for the process lifetime.
static void beginSupervisorActivity(void) {
	@autoreleasepool {
		if (autoActivityToken != nil) {
			return;
		}
		NSActivityOptions opts = NSActivityUserInitiatedAllowingIdleSystemSleep;
		autoActivityToken = [[[NSProcessInfo processInfo]
			beginActivityWithOptions:opts
			                  reason:@"Auto supervises background services"] retain];
	}
}
*/
import "C"

// disableAppNap prevents macOS from throttling the supervision loop while the
// daemon runs in the background. Idempotent; safe from any goroutine.
func disableAppNap() {
	C.beginSupervisorActivity()
}
