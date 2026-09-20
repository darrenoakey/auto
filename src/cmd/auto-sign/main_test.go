package main

import (
	"strings"
	"testing"
)

func TestRunRejectsInvalidArguments(t *testing.T) {
	for _, arguments := range [][]string{nil, {"sign"}, {"unknown", "/tmp/Auto.app"}} {
		if err := run(arguments); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("run(%q) = %v, want usage error", arguments, err)
		}
	}
}
