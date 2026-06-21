package main

import "testing"

func TestOptPort(t *testing.T) {
	got, err := optPort(&parsedArgs{values: map[string]string{"port": "8080"}})
	if err != nil || got == nil || *got != 8080 {
		t.Fatalf("optPort = %v, %v", got, err)
	}
	if v, err := optPort(&parsedArgs{values: map[string]string{}}); err != nil || v != nil {
		t.Fatalf("absent port should be nil, got %v, %v", v, err)
	}
	if _, err := optPort(&parsedArgs{values: map[string]string{"port": "x"}}); err == nil {
		t.Fatal("non-integer port should error")
	}
}

func TestOptStr(t *testing.T) {
	p := &parsedArgs{values: map[string]string{"command": "sleep 1"}}
	if got := optStr(p, "command"); got == nil || *got != "sleep 1" {
		t.Fatalf("optStr = %v", got)
	}
	if optStr(p, "workdir") != nil {
		t.Fatal("absent value should be nil")
	}
}
