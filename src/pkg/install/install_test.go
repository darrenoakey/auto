package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"auto/pkg/signing"
)

const installTestInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.darrenoakey.auto</string>
<key>CFBundleExecutable</key><string>auto</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
`

func TestAppExePath(t *testing.T) {
	got := AppExePath("/Users/x/local/auto")
	want := "/Users/x/local/auto/output/Auto.app/Contents/MacOS/auto"
	if got != want {
		t.Fatalf("AppExePath = %s, want %s", got, want)
	}
}

func TestPlistContentRendersAndIsValid(t *testing.T) {
	appExe := "/Users/x/local/auto/output/Auto.app/Contents/MacOS/auto"
	logPath := "/Users/x/local/auto/output/logs/auto/auto.log"
	content := plistContent(appExe, logPath)
	for _, must := range []string{appExe, logPath, "com.darrenoakey.auto", "<string>watch</string>", "LANG", daemonPATH} {
		if !strings.Contains(content, must) {
			t.Fatalf("plist missing %q:\n%s", must, content)
		}
	}
	// Validate it as a real plist via plutil.
	tmp := filepath.Join(t.TempDir(), "test.plist")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("plutil", "-lint", tmp).CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected generated plist: %s", out)
	}
}

func TestWriteWrapperExecsAppBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	appExe := "/Users/x/local/auto/output/Auto.app/Contents/MacOS/auto"
	if err := writeWrapper(appExe); err != nil {
		t.Fatalf("writeWrapper: %v", err)
	}
	wrapper := filepath.Join(home, "bin", "auto")
	data, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatalf("read wrapper: %v", err)
	}
	if !strings.Contains(string(data), "exec \""+appExe+"\"") {
		t.Fatalf("wrapper does not exec app binary:\n%s", data)
	}
	info, err := os.Stat(wrapper)
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("wrapper should be executable: mode=%v err=%v", info.Mode(), err)
	}
}

func TestWriteWrapperUnlinksExistingSymlinkWithoutTruncatingTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	appExe := filepath.Join(t.TempDir(), "target-auto")
	originalContent := []byte("precious-signed-binary-content")
	if err := os.WriteFile(appExe, originalContent, 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	wrapper := filepath.Join(binDir, "auto")
	if err := os.Symlink(appExe, wrapper); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := writeWrapper(appExe); err != nil {
		t.Fatalf("writeWrapper: %v", err)
	}
	targetData, err := os.ReadFile(appExe)
	if err != nil || string(targetData) != string(originalContent) {
		t.Fatalf("writeWrapper corrupted/truncated symlink target: %q", string(targetData))
	}
	wrapperData, err := os.ReadFile(wrapper)
	if err != nil || !strings.Contains(string(wrapperData), "exec \""+appExe+"\"") {
		t.Fatalf("wrapper content incorrect: %s", wrapperData)
	}
}

func TestInstallFailsWithoutSignedBinary(t *testing.T) {
	if _, err := verifiedAppExecutable(t.TempDir()); err == nil {
		t.Fatal("installation verification should fail when the signed binary is missing")
	}
}

func TestInstallRejectsUnsignedBundleBeforeFilesystemSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	appExe := AppExePath(root)
	if err := os.MkdirAll(filepath.Dir(appExe), 0o755); err != nil {
		t.Fatalf("create unsigned bundle: %v", err)
	}
	if err := os.WriteFile(appExe, []byte("unsigned"), 0o755); err != nil {
		t.Fatalf("write unsigned executable: %v", err)
	}
	if _, err := verifiedAppExecutable(root); err == nil {
		t.Fatal("installation verification accepted an unsigned bundle")
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "auto")); !os.IsNotExist(err) {
		t.Fatalf("failed verification changed wrapper state: %v", err)
	}
}

func TestInstallRejectsTamperedSignedBundleBeforeFilesystemSideEffects(t *testing.T) {
	root := t.TempDir()
	buildSignedInstallTestBundle(t, root)
	appExe := AppExePath(root)
	executable, err := os.OpenFile(appExe, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open signed executable: %v", err)
	}
	value := []byte{0}
	if _, err = executable.ReadAt(value, 4096); err != nil {
		_ = executable.Close()
		t.Fatalf("read signed executable byte: %v", err)
	}
	value[0] ^= 0xff
	if _, err = executable.WriteAt(value, 4096); err != nil {
		_ = executable.Close()
		t.Fatalf("tamper signed executable: %v", err)
	}
	if err = executable.Close(); err != nil {
		t.Fatalf("close tampered executable: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := verifiedAppExecutable(root); err == nil {
		t.Fatal("installation verification accepted a tampered signed bundle")
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "auto")); !os.IsNotExist(err) {
		t.Fatalf("failed verification changed wrapper state: %v", err)
	}
}

func buildSignedInstallTestBundle(t *testing.T, root string) {
	t.Helper()
	executablePath := AppExePath(root)
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("create signed test bundle: %v", err)
	}
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	command := exec.Command("go", "build", "-ldflags", "-s -w", "-o", executablePath, "./pkg/signing/testdata/minimal")
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real Mach-O: %v: %s", err, output)
	}
	infoPath := filepath.Join(root, "output", "Auto.app", "Contents", "Info.plist")
	if err := os.WriteFile(infoPath, []byte(installTestInfoPlist), 0o644); err != nil {
		t.Fatalf("write test Info.plist: %v", err)
	}
	if err := signing.SignBundle(context.Background(), filepath.Dir(filepath.Dir(filepath.Dir(executablePath)))); err != nil {
		t.Fatalf("sign real install test bundle: %v", err)
	}
}
