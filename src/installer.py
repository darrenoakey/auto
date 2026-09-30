from __future__ import annotations

import os
import signal
import subprocess
import time
from pathlib import Path

import signing

LAUNCHAGENT_LABEL = "com.darrenoakey.auto"


# ##################################################################
# get project root
# returns the absolute path to the auto project directory
def get_project_root() -> Path:
    return Path(__file__).parent.parent.resolve()


# ##################################################################
# check bin in path
# verifies that ~/bin is in the user's PATH environment variable
def check_bin_in_path() -> bool:
    path_env = os.environ.get("PATH", "")
    home = Path.home()
    bin_dir = home / "bin"

    for path_entry in path_env.split(":"):
        if Path(path_entry).resolve() == bin_dir:
            return True
    return False


# ##################################################################
# create bin wrapper
# creates or updates the ~/bin/auto script that delegates to run
def create_bin_wrapper() -> None:
    home = Path.home()
    bin_dir = home / "bin"
    auto_script = bin_dir / "auto"
    project_root = get_project_root()
    run_script = project_root / "run"

    if not bin_dir.exists():
        raise RuntimeError(f"{bin_dir} does not exist. Create it first and add it to your PATH.")

    content = f"""#!/bin/bash
# ##################################################################
# auto wrapper
# delegates all commands to the run script in the auto project directory
exec {signing.stable_python_path()} {run_script} "$@"
"""

    auto_script.write_text(content)
    auto_script.chmod(0o755)
    print(f"Created {auto_script}")


# ##################################################################
# handover marker path
# while this file exists a SIGTERM to the watch daemon means "a new daemon is
# taking over": exit without shutting down the managed processes
def handover_marker_path() -> Path:
    return get_project_root() / "local" / "daemon-handover"


# ##################################################################
# create launchagent plist
# creates or updates the LaunchAgent plist for auto-starting the daemon.
# launchd runs the signed launcher, which runs python, so TCC keys every grant
# on the launcher's stable code identity rather than on python's
def create_launchagent_plist(launcher: Path) -> Path:
    home = Path.home()
    launch_agents_dir = home / "Library" / "LaunchAgents"
    launch_agents_dir.mkdir(parents=True, exist_ok=True)

    plist_path = launch_agents_dir / f"{LAUNCHAGENT_LABEL}.plist"
    project_root = get_project_root()
    run_script = project_root / "run"
    output_dir = project_root / "output" / "logs"
    output_dir.mkdir(parents=True, exist_ok=True)

    stdout_log = output_dir / "auto.stdout.log"
    stderr_log = output_dir / "auto.stderr.log"

    # build PATH from current environment
    path_env = os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
    pythonpath_env = os.environ.get("PYTHONPATH", "")
    lang_env = os.environ.get("LANG", "")

    plist_content = f"""<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{LAUNCHAGENT_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{launcher}</string>
        <string>{signing.stable_python_path()}</string>
        <string>{run_script}</string>
        <string>watch</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{stdout_log}</string>
    <key>StandardErrorPath</key>
    <string>{stderr_log}</string>
    <key>KeepAlive</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{path_env}</string>"""

    if pythonpath_env:
        plist_content += f"""
        <key>PYTHONPATH</key>
        <string>{pythonpath_env}</string>"""

    if lang_env:
        plist_content += f"""
        <key>LANG</key>
        <string>{lang_env}</string>"""

    plist_content += """
    </dict>
</dict>
</plist>
"""

    plist_path.write_text(plist_content)
    print(f"Created {plist_path}")
    return plist_path


# ##################################################################
# launchagent domain
# the launchd domain the agent lives in for the current user
def launchagent_domain() -> str:
    return f"gui/{os.getuid()}"


# ##################################################################
# running daemon pid
# pid launchd reports for the auto agent, or None when it is not running
def running_daemon_pid() -> int | None:
    result = subprocess.run(
        ["launchctl", "print", f"{launchagent_domain()}/{LAUNCHAGENT_LABEL}"],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        stripped = line.strip()
        if stripped.startswith("pid = "):
            return int(stripped.split("=", 1)[1].strip())
    return None


# ##################################################################
# wait for pid exit
# blocks until the pid is gone or the deadline passes
def wait_for_pid_exit(pid: int, timeout: float) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.1)
    return False


# ##################################################################
# process executable
# path of the executable behind a pid, or None if the pid is gone
def process_executable(pid: int) -> str | None:
    result = subprocess.run(["ps", "-o", "comm=", "-p", str(pid)], capture_output=True, text=True, check=False)
    executable = result.stdout.strip()
    return executable or None


# ##################################################################
# load launchagent
# replaces the running daemon with the one described by the plist WITHOUT
# stopping the managed processes. A launcher-based daemon understands the
# handover marker and treats launchd's SIGTERM as "exit, leave children
# running". An older python-direct daemon would shut everything down on
# SIGTERM, so it is frozen, booted out, and SIGKILLed before it can react.
def load_launchagent(plist_path: Path, launcher: Path) -> None:
    marker = handover_marker_path()
    marker.parent.mkdir(parents=True, exist_ok=True)
    marker.write_text(str(os.getpid()))
    try:
        old_pid = running_daemon_pid()
        old_style = old_pid is not None and process_executable(old_pid) != str(launcher)
        if old_style:
            os.kill(old_pid, signal.SIGSTOP)
        subprocess.run(
            ["launchctl", "bootout", f"{launchagent_domain()}/{LAUNCHAGENT_LABEL}"],
            check=False,
            capture_output=True,
        )
        if old_style:
            kill_daemon_without_handover(old_pid)
        if old_pid is not None and not wait_for_pid_exit(old_pid, 15):
            raise RuntimeError(f"old auto daemon (pid {old_pid}) did not exit after bootout")

        result = subprocess.run(
            ["launchctl", "bootstrap", launchagent_domain(), str(plist_path)],
            check=False,
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise RuntimeError(f"Failed to load LaunchAgent: {result.stderr.strip()}")
    finally:
        marker.unlink(missing_ok=True)

    print(f"Loaded LaunchAgent: {plist_path}")


# ##################################################################
# kill daemon without handover
# SIGKILL a frozen old-style daemon so its pending SIGTERM handler never runs;
# managed processes live in their own sessions and survive
def kill_daemon_without_handover(pid: int) -> None:
    try:
        os.kill(pid, signal.SIGKILL)
    except ProcessLookupError:
        return
    if not wait_for_pid_exit(pid, 10):
        raise RuntimeError(f"old auto daemon (pid {pid}) survived SIGKILL")


# ##################################################################
# verify installation
# checks that the installation is working correctly
def verify_installation() -> None:
    home = Path.home()
    auto_script = home / "bin" / "auto"
    plist_path = home / "Library" / "LaunchAgents" / "com.darrenoakey.auto.plist"

    # check bin/auto exists and is executable
    if not auto_script.exists():
        raise RuntimeError(f"{auto_script} does not exist")
    if not os.access(auto_script, os.X_OK):
        raise RuntimeError(f"{auto_script} is not executable")

    # check plist exists
    if not plist_path.exists():
        raise RuntimeError(f"{plist_path} does not exist")

    # check the daemon is running under launchd
    deadline = time.time() + 15
    pid = running_daemon_pid()
    while pid is None and time.time() < deadline:
        time.sleep(0.2)
        pid = running_daemon_pid()
    if pid is None:
        raise RuntimeError("LaunchAgent is loaded but the daemon is not running")

    # the launchd process must be the signed launcher, never python itself
    executable = process_executable(pid)
    if executable != str(signing.default_launcher_path()):
        raise RuntimeError(f"daemon pid {pid} is {executable}, not the signed launcher")

    print(f"Installation verified successfully (daemon pid {pid} is the signed launcher)")


# ##################################################################
# install
# performs complete installation of auto daemon and wrapper script
def install() -> None:
    print("Installing auto...")

    # check PATH
    if not check_bin_in_path():
        print("Warning: ~/bin is not in your PATH")
        print("Add this to your shell profile:")
        print('  export PATH="$HOME/bin:$PATH"')
        print()

    # create bin wrapper
    create_bin_wrapper()

    # build and sign the launcher with the persistent identity
    launcher, requirement = signing.ensure_signed_launcher()
    print(f"Signed launcher {launcher}")
    print(f"  designated requirement: {requirement}")

    # create and load LaunchAgent, handing over from the running daemon
    plist_path = create_launchagent_plist(launcher)
    load_launchagent(plist_path, launcher)

    # verify everything worked
    verify_installation()

    print()
    print("Installation complete!")
    print("You can now use 'auto' from anywhere in your terminal")
    print("The daemon will start automatically on login")
    print()
    print("Grant macOS permissions (Full Disk Access, Accessibility, ...) to:")
    print(f"  {launcher}")
    print("They persist across every future rebuild of auto and python.")
