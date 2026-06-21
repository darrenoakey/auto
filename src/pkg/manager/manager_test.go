package manager

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTestManager returns a Manager rooted at a fresh temporary directory.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return New(t.TempDir())
}

// mustAdd registers a process definition or fails the test.
func mustAdd(t *testing.T, m *Manager, name, command string, port *int) {
	t.Helper()
	if err := m.AddProcess(name, command, port, ""); err != nil {
		t.Fatalf("AddProcess(%q): %v", name, err)
	}
}

// intPtr returns a pointer to an int literal for table tests.
func intPtr(v int) *int { return &v }

func TestDefaultRootUnderHome(t *testing.T) {
	m := Default()
	if !strings.HasSuffix(m.Root(), filepath.Join("local", "auto")) {
		t.Fatalf("Default root = %q, want suffix local/auto", m.Root())
	}
}

func TestRootReturnsConfiguredPath(t *testing.T) {
	root := t.TempDir()
	if got := New(root).Root(); got != root {
		t.Fatalf("Root() = %q, want %q", got, root)
	}
}
