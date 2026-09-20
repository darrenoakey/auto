package main

import (
	"auto/pkg/manager"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
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

func TestRestartOverlapModesAreMutuallyExclusive(t *testing.T) {
	manager := manager.New(t.TempDir())
	if code := dispatch(manager, "restart", []string{"svc", "--overlap", "--strict-overlap"}); code != 1 {
		t.Fatalf("dispatch returned %d, want 1", code)
	}
}

func TestUsageDocumentsStrictOverlap(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	usage()
	os.Stdout = original
	if err := writer.Close(); err != nil {
		t.Fatalf("close usage writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if !strings.Contains(string(output), "--overlap|--strict-overlap") {
		t.Fatalf("usage missing strict overlap: %q", output)
	}
}

func TestBundleDeclaresRemovableVolumePurpose(t *testing.T) {
	const plistPath = "bundle/Info.plist"
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("stat bundle metadata: %v", err)
	}
	output, err := exec.Command(
		"plutil",
		"-extract", "NSRemovableVolumesUsageDescription",
		"raw",
		"-o", "-",
		plistPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("read removable-volume purpose: %v: %s", err, output)
	}
	if purpose := strings.TrimSpace(string(output)); !strings.Contains(purpose, "model data") {
		t.Fatalf("removable-volume purpose = %q, want model-data explanation", purpose)
	}
}
