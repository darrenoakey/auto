package main

import (
	"testing"

	"auto/pkg/manager"
)

func TestPidColumn(t *testing.T) {
	running := manager.ProcessInfo{Running: true, Pid: 1234}
	if pidColumn(running) != "1234" {
		t.Fatalf("running = %q", pidColumn(running))
	}
	stopped := manager.ProcessInfo{Running: false, ExplicitlyStopped: true}
	if pidColumn(stopped) != "stopped" {
		t.Fatalf("stopped = %q", pidColumn(stopped))
	}
	dead := manager.ProcessInfo{Running: false}
	if pidColumn(dead) != "dead" {
		t.Fatalf("dead = %q", pidColumn(dead))
	}
}

func TestPortColumn(t *testing.T) {
	port := 8080
	if portColumn(manager.ProcessInfo{Port: &port}) != "8080" {
		t.Fatal("port column wrong")
	}
	if portColumn(manager.ProcessInfo{}) != "-" {
		t.Fatal("missing port should be -")
	}
}

func TestRestartColumn(t *testing.T) {
	iv := 86400
	if restartColumn(manager.ProcessInfo{RestartInterval: &iv}) != "1d" {
		t.Fatal("restart column wrong")
	}
	if restartColumn(manager.ProcessInfo{}) != "-" {
		t.Fatal("missing interval should be -")
	}
}

func TestCmdStopAllNoRunning(t *testing.T) {
	m := manager.New(t.TempDir())
	if code := cmdStopAll(m); code != 0 {
		t.Fatalf("cmdStopAll with nothing running = %d, want 0", code)
	}
}

func TestReportRestart(t *testing.T) {
	if reportRestart("svc", "pid 42") {
		t.Fatal("a pid result should report success (false)")
	}
	if !reportRestart("svc", "boom: cannot start") {
		t.Fatal("an error result should report failure (true)")
	}
}
