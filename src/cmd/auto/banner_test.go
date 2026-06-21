package main

import "testing"

func TestRunningUnderAgent(t *testing.T) {
	for _, v := range aiAgentEnvVars {
		t.Setenv(v, "")
	}
	if runningUnderAgent() {
		t.Fatal("no agent vars set should report false")
	}
	t.Setenv("CLAUDECODE", "1")
	if !runningUnderAgent() {
		t.Fatal("CLAUDECODE set should report true")
	}
}
