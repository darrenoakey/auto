// Package install wires the auto watch daemon into launchd and installs the
// ~/bin/auto wrapper. The LaunchAgent execs the signed Auto.app binary directly
// (no bash launcher, not under any other supervisor) so macOS attributes Local
// Network access to the bundle's stable code identity — the grant the user makes
// once in System Settings then persists across rebuilds, and every child the
// daemon spawns inherits it.
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"auto/pkg/manager"
)

// daemonPATH is the PATH given to the watch daemon and every process it spawns.
// It is fixed (not read from the environment) so behaviour does not depend on
// whoever ran the installer.
const daemonPATH = "/Users/darrenoakey/.local/bin:/Users/darrenoakey/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// AppExePath returns the path to the signed app binary inside the project tree.
func AppExePath(root string) string {
	return filepath.Join(root, "output", "Auto.app", "Contents", "MacOS", "auto")
}

// Install writes the wrapper and LaunchAgent and (re)loads the daemon. The app
// bundle must already be built and signed (see `run build`).
func Install(root string) error {
	appExe := AppExePath(root)
	if _, err := os.Stat(appExe); err != nil {
		return fmt.Errorf("signed app binary missing at %s (run `run build` first): %w", appExe, err)
	}
	if err := writeWrapper(appExe); err != nil {
		return err
	}
	plist, err := writeLaunchAgent(root, appExe)
	if err != nil {
		return err
	}
	if err := reloadAgent(plist); err != nil {
		return err
	}
	fmt.Println("Installed and loaded auto LaunchAgent (signed, Local Network capable).")
	return nil
}

// writeWrapper installs ~/bin/auto as a thin exec into the signed app binary so
// CLI invocations run the same signed identity.
func writeWrapper(appExe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("#!/bin/bash\nexec %q \"$@\"\n", appExe)
	wrapper := filepath.Join(binDir, "auto")
	if err := os.WriteFile(wrapper, []byte(content), 0o755); err != nil {
		return err
	}
	fmt.Printf("Created %s\n", wrapper)
	return nil
}

// writeLaunchAgent renders and writes the daemon plist, returning its path.
func writeLaunchAgent(root, appExe string) (string, error) {
	logPath := filepath.Join(root, "output", "logs", "auto", "auto.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", err
	}
	plist := manager.LaunchAgentPath()
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(plist, []byte(plistContent(appExe, logPath)), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("plutil", "-lint", plist).CombinedOutput(); err != nil {
		return "", fmt.Errorf("invalid plist: %s", out)
	}
	fmt.Printf("Created %s\n", plist)
	return plist, nil
}

// plistTemplate is the LaunchAgent plist. The daemon runs in the Aqua session so
// its children share the GUI session's Local Network context. Substitution order:
// label, app exe, stdout path, stderr path, PATH.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>watch</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>LimitLoadToSessionType</key><string>Aqua</string>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key><string>%s</string>
        <key>LANG</key><string>en_AU.UTF-8</string>
    </dict>
</dict>
</plist>
`

// plistContent renders the LaunchAgent plist for the given binary and log path.
func plistContent(appExe, logPath string) string {
	return fmt.Sprintf(plistTemplate, manager.LaunchAgentLabel, appExe, logPath, logPath, daemonPATH)
}

// reloadAgent boots out any running instance and bootstraps the agent fresh so
// the running daemon is the just-installed binary. Bootout sends SIGTERM, which
// routes through the daemon's own clean teardown of managed processes (a
// launchd kickstart would SIGKILL the whole job tree instead); the fresh daemon
// then restarts everything under its signed identity.
func reloadAgent(plist string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	target := domain + "/" + manager.LaunchAgentLabel
	_ = exec.Command("launchctl", "bootout", target).Run()
	for i := 0; i < 50; i++ {
		if exec.Command("launchctl", "print", target).Run() != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("bootstrap failed: %s", out)
	}
	return nil
}
