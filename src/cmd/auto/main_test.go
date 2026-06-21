package main

import (
	"reflect"
	"testing"
)

func TestExtractGlobalFlags(t *testing.T) {
	rest, quiet, banner := extractGlobalFlags([]string{"-q", "ps"})
	if !quiet || banner || !reflect.DeepEqual(rest, []string{"ps"}) {
		t.Fatalf("got rest=%v quiet=%v banner=%v", rest, quiet, banner)
	}
	rest, quiet, banner = extractGlobalFlags([]string{"add", "svc", "--banner", "sleep"})
	if quiet || !banner || !reflect.DeepEqual(rest, []string{"add", "svc", "sleep"}) {
		t.Fatalf("got rest=%v quiet=%v banner=%v", rest, quiet, banner)
	}
}

func TestToSet(t *testing.T) {
	set := toSet([]string{"a", "b"})
	if !set["a"] || !set["b"] || set["c"] {
		t.Fatalf("toSet wrong: %v", set)
	}
}

func TestRunWithNoArgsReturnsUsageCode(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("run(nil) = %d, want 2", code)
	}
}
