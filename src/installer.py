import os
import subprocess
from pathlib import Path


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
exec {run_script} "$@"
"""

    auto_script.write_text(content)
    auto_script.chmod(0o755)
    print(f"Created {auto_script}")


# ##################################################################
# create launchagent plist
# creates or updates the LaunchAgent plist for auto-starting the daemon
def create_launchagent_plist() -> Path:
    home = Path.home()
    launch_agents_dir = home / "Library" / "LaunchAgents"
    launch_agents_dir.mkdir(parents=True, exist_ok=True)

    plist_path = launch_agents_dir / "com.darrenoakey.auto.plist"
    project_root = get_project_root()
    run_script = project_root / "run"
    output_dir = project_root / "output" / "logs"
    output_dir.mkdir(parents=True, exist_ok=True)

    stdout_log = output_dir / "auto.stdout.log"
    stderr_log = output_dir / "auto.stderr.log"

    # build PATH from current environment
    path_env = os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
    pythonpath_env = os.environ.get("PYTHONPATH", "")

    plist_content = f"""<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.darrenoakey.auto</string>
    <key>ProgramArguments</key>
    <array>
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

    plist_content += """
    </dict>
</dict>
</plist>
"""

    plist_path.write_text(plist_content)
    print(f"Created {plist_path}")
    return plist_path


# ##################################################################
# load launchagent
# loads the LaunchAgent plist into launchctl so it runs on login
def load_launchagent(plist_path: Path) -> None:
    # unload first if already loaded
    try:
        subprocess.run(
            ["launchctl", "unload", str(plist_path)],
            check=False,
            capture_output=True
        )
    except Exception:
        pass

    # load the plist
    result = subprocess.run(
        ["launchctl", "load", str(plist_path)],
        check=False,
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        raise RuntimeError(f"Failed to load LaunchAgent: {result.stderr}")

    print(f"Loaded LaunchAgent: {plist_path}")


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

    # check if loaded in launchctl
    result = subprocess.run(
        ["launchctl", "list"],
        capture_output=True,
        text=True,
        check=True
    )

    if "com.darrenoakey.auto" not in result.stdout:
        raise RuntimeError("LaunchAgent not loaded in launchctl")

    print("Installation verified successfully")


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

    # create and load LaunchAgent
    plist_path = create_launchagent_plist()
    load_launchagent(plist_path)

    # verify everything worked
    verify_installation()

    print()
    print("Installation complete!")
    print("You can now use 'auto' from anywhere in your terminal")
    print("The daemon will start automatically on login")
