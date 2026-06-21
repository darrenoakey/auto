package main

import "testing"

// TestDisableAppNapIdempotent verifies the App Nap opt-out can be called safely
// and repeatedly without panicking.
func TestDisableAppNapIdempotent(t *testing.T) {
	disableAppNap()
	disableAppNap()
}
