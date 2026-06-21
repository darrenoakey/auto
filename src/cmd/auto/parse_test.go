package main

import "testing"

func TestParseArgsPositionalsAndFlags(t *testing.T) {
	p, err := parseArgs(
		[]string{"name", "python", "server.py", "--port", "8080", "--restart-every=24h"},
		valueSet("port", "restart-every"), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.positional) != 3 || p.positional[0] != "name" {
		t.Fatalf("positionals wrong: %v", p.positional)
	}
	if p.values["port"] != "8080" || p.values["restart-every"] != "24h" {
		t.Fatalf("values wrong: %v", p.values)
	}
}

func TestParseArgsBoolFlags(t *testing.T) {
	p, err := parseArgs([]string{"svc", "--tail"}, nil, boolSet("file", "tail"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.bools["tail"] || p.bools["file"] {
		t.Fatalf("bools wrong: %v", p.bools)
	}
}

func TestParseArgsMissingValueErrors(t *testing.T) {
	if _, err := parseArgs([]string{"--port"}, valueSet("port"), nil); err == nil {
		t.Fatal("missing flag value should error")
	}
}

func TestParseArgsUnknownFlagErrors(t *testing.T) {
	if _, err := parseArgs([]string{"--bogus"}, nil, nil); err == nil {
		t.Fatal("unknown flag should error")
	}
}

func TestRequireName(t *testing.T) {
	p := &parsedArgs{positional: []string{"svc"}}
	if name, err := p.requireName(); err != nil || name != "svc" {
		t.Fatalf("requireName = %q, %v", name, err)
	}
	empty := &parsedArgs{}
	if _, err := empty.requireName(); err == nil {
		t.Fatal("empty positionals should error")
	}
}
