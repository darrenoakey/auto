package signing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.darrenoakey.auto</string>
<key>CFBundleExecutable</key><string>auto</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
`

// This exercises the real encrypted provider, production Developer ID
// identity, rcodesign, Apple timestamp service, and Mach-O verifier together.
func TestSignBundleSignsRealMachOWithStableIdentity(t *testing.T) {
	bundlePath := buildTestBundle(t)
	if err := SignBundle(context.Background(), bundlePath); err != nil {
		t.Fatalf("sign real Mach-O app: %v", err)
	}
	if err := VerifyBundle(context.Background(), bundlePath); err != nil {
		t.Fatalf("verify real Mach-O app: %v", err)
	}
}

func TestLoadCredentialsMissingReferenceFailsClosed(t *testing.T) {
	reference := absentSecretReference(t)
	references := credentialReferences{p12: reference, password: reference}
	loaded, err := loadCredentials(context.Background(), references)
	if loaded != nil || err == nil {
		t.Fatal("missing signing reference must return no credentials and an error")
	}
	if !strings.Contains(err.Error(), reference.service+"/"+reference.account) || !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Fatalf("missing reference error lacks its nonsecret identity: %v", err)
	}
}

func TestVerifySignatureInfoRejectsWrongIdentity(t *testing.T) {
	output := "identifier: com.example.wrong\nteam_name: WRONGTEAM\nsignature_verifies: true\napple_certificate_profile: developer-id-application\n"
	if err := verifySignatureInfo(output); err == nil {
		t.Fatal("unexpected signature identity must fail")
	}
}

func TestStableDesignatedRequirementRejectsCertificateHashIdentity(t *testing.T) {
	requirement := `designated(3): (identifier "com.darrenoakey.auto") and (cdhash H"0011")`
	if stableDesignatedRequirement(requirement) {
		t.Fatal("certificate hash requirement would change across rebuilds or renewals")
	}
}

func TestBundleSigningFailurePreservesExistingBundle(t *testing.T) {
	repositoryRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	testRoot := t.TempDir()
	copyTestFile(t, filepath.Join(repositoryRoot, "run"), filepath.Join(testRoot, "run"), 0o755)
	infoPath := filepath.Join(testRoot, "src", "cmd", "auto", "bundle", "Info.plist")
	writeTestFile(t, infoPath, []byte(testInfoPlist), 0o644)
	helperPath := filepath.Join(testRoot, "output", "bin", "auto-sign")
	if err := os.MkdirAll(filepath.Dir(helperPath), 0o755); err != nil {
		t.Fatalf("create helper directory: %v", err)
	}
	build := exec.Command("go", "build", "-o", helperPath, "./cmd/auto-sign")
	build.Dir = filepath.Join(repositoryRoot, "src")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real signing helper: %v: %s", err, output)
	}
	writeTestFile(t, filepath.Join(testRoot, "output", "bin", "auto"), []byte("not a Mach-O"), 0o755)
	markerPath := filepath.Join(testRoot, "output", "Auto.app", "preserved")
	writeTestFile(t, markerPath, []byte("prior verified bundle"), 0o644)
	command := exec.Command(filepath.Join(testRoot, "run"), "bundle")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("invalid Mach-O unexpectedly signed: %s", output)
	}
	if value, err := os.ReadFile(markerPath); err != nil || string(value) != "prior verified bundle" {
		t.Fatalf("signing failure replaced existing bundle: value=%q err=%v", value, err)
	}
	stages, err := filepath.Glob(filepath.Join(testRoot, "output", ".auto-bundle-stage.*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("failed signing left staging state: stages=%v err=%v", stages, err)
	}
}

func buildTestBundle(t *testing.T) string {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "Auto.app")
	executablePath := filepath.Join(bundlePath, "Contents", "MacOS", executableName)
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("create test app: %v", err)
	}
	command := exec.Command("go", "build", "-ldflags", "-s -w", "-o", executablePath, "./testdata/minimal")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build test Mach-O: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "Contents", "Info.plist"), []byte(testInfoPlist), 0o644); err != nil {
		t.Fatalf("write test Info.plist: %v", err)
	}
	return bundlePath
}

func absentSecretReference(t *testing.T) secretReference {
	t.Helper()
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("create absent secret namespace: %v", err)
	}
	return secretReference{service: "auto-sign-test-absent-" + hex.EncodeToString(random), account: "missing"}
}

func copyTestFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	value, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	writeTestFile(t, destination, value, mode)
}

func writeTestFile(t *testing.T, path string, value []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, value, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
