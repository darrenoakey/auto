package manager

import "testing"

func TestParseInterval(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"Seconds", "90s", 90, false},
		{"Minutes", "30m", 1800, false},
		{"Hours", "12h", 43200, false},
		{"Days", "7d", 604800, false},
		{"BareSeconds", "120", 120, false},
		{"Fractional", "1.5h", 5400, false},
		{"Empty", "", 0, true},
		{"Garbage", "abc", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInterval(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseInterval(%q) expected error", tt.in)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseInterval(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestFormatInterval(t *testing.T) {
	tests := []struct {
		seconds int
		want    string
	}{
		{86400, "1d"},
		{604800, "7d"},
		{43200, "12h"},
		{1800, "30m"},
		{90, "90s"},
		{0, "0s"},
	}
	for _, tt := range tests {
		if got := FormatInterval(tt.seconds); got != tt.want {
			t.Errorf("FormatInterval(%d) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestSetAndGetRestartInterval(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	if m.GetRestartInterval("svc") != nil {
		t.Fatal("expected no interval initially")
	}
	if err := m.SetRestartInterval("svc", intPtr(86400)); err != nil {
		t.Fatalf("set: %v", err)
	}
	if iv := m.GetRestartInterval("svc"); iv == nil || *iv != 86400 {
		t.Fatalf("interval = %v, want 86400", iv)
	}
	if err := m.SetRestartInterval("svc", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if m.GetRestartInterval("svc") != nil {
		t.Fatal("interval should be cleared")
	}
}

func TestNeedsPeriodicRestart(t *testing.T) {
	m := newTestManager(t)
	mustAdd(t, m, "svc", "sleep 1", nil)
	pid := 4242
	setRuntime(t, m, "svc", func(p *Process) {
		p.Pid = &pid
		p.RestartIntervalSeconds = intPtr(3600)
		recent := nowUnix()
		p.LastPeriodicRestart = &recent
	})
	if m.needsPeriodicRestart("svc") {
		t.Fatal("recently restarted process is not due")
	}
	setRuntime(t, m, "svc", func(p *Process) {
		old := nowUnix() - 7200
		p.LastPeriodicRestart = &old
	})
	if !m.needsPeriodicRestart("svc") {
		t.Fatal("process past its interval should be due")
	}
}
