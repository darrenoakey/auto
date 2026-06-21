package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestInstallFailsWithoutSignedBinary(t *testing.T) {
	// Root with no built app: Install must refuse rather than bootstrap nothing.
	if err := Install(t.TempDir()); err == nil {
		t.Fatal("Install should fail when the signed binary is missing")
	}
}
