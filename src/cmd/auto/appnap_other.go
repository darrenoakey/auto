//go:build !darwin

package main

// disableAppNap is a no-op on non-darwin platforms, which have no App Nap.
func disableAppNap() {}
