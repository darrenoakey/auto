package signing

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func TestProcessErrorSuppressesAllSecretBearingOutput(t *testing.T) {
	err := processError("tool", errors.New("failed"), "partial-or-encoded-secret", true)
	if strings.Contains(err.Error(), "partial-or-encoded-secret") {
		t.Fatal("process error exposed secret-bearing child output")
	}
}

func TestRunProcessSuppressesOutputWhenSecretDescriptorIsPresent(t *testing.T) {
	marker := make([]byte, 32)
	if _, err := rand.Read(marker); err != nil {
		t.Fatalf("create protocol marker: %v", err)
	}
	output, err := runProcess(context.Background(), "/bin/cat", []string{"/dev/fd/3"}, marker)
	if err != nil {
		t.Fatalf("read real anonymous descriptor: %v", err)
	}
	if output != "" {
		t.Fatal("secret-bearing process returned child output")
	}
}

func TestRunProcessSuppressesErrorOutputWhenSecretDescriptorIsPresent(t *testing.T) {
	marker := make([]byte, 32)
	if _, err := rand.Read(marker); err != nil {
		t.Fatalf("create protocol marker: %v", err)
	}
	_, err := runProcess(context.Background(), "/bin/sh", []string{"-c", "cat /dev/fd/3; exit 7"}, marker)
	if err == nil {
		t.Fatal("failing real child returned success")
	}
	if strings.Contains(err.Error(), string(marker)) {
		t.Fatal("secret-bearing process error exposed child output")
	}
}

func TestBoundedWriterLimitsCapturedOutput(t *testing.T) {
	writer := &boundedWriter{limit: 4}
	if count, err := writer.Write([]byte("abcdefgh")); err != nil || count != 8 {
		t.Fatalf("bounded write = %d, %v", count, err)
	}
	if writer.String() != "abcd" {
		t.Fatalf("bounded output = %q", writer.String())
	}
}
