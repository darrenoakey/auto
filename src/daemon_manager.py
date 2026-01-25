#!/usr/bin/env python3
import json
import os
import shutil
import signal
import subprocess
import time
from datetime import datetime
from pathlib import Path
from typing import Optional


# ##################################################################
# get project root
# finds the absolute path to the auto project directory
def get_project_root() -> Path:
    return Path(__file__).parent.parent.absolute()


# ##################################################################
# get state path
# returns the path to the single state file that stores both process definitions and runtime state
def get_state_path() -> Path:
    return get_project_root() / "local" / "state.json"


# ##################################################################
# get log dir
# returns the directory where process logs are stored
def get_log_dir() -> Path:
    log_dir = get_project_root() / "output" / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir


# ##################################################################
# get auto log path
# returns the path for the auto daemon log file
def get_auto_log_path() -> Path:
    log_dir = get_log_dir() / "auto"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir / "auto.log"


# ##################################################################
# migrate legacy logs
# moves old flat log files into an old archive directory once
def migrate_legacy_logs() -> None:
    log_dir = get_log_dir()
    marker_path = log_dir / ".migrated"
    if marker_path.exists():
        return

    legacy_files = [
        path for path in log_dir.iterdir()
        if path.is_file() and path.name != marker_path.name
    ]

    legacy_dir = log_dir / "old"
    if legacy_files:
        legacy_dir.mkdir(parents=True, exist_ok=True)
        for path in legacy_files:
            destination = _ensure_unique_path(legacy_dir / path.name)
            shutil.move(str(path), destination)

    previous_legacy = log_dir / "_legacy"
    if previous_legacy.exists():
        legacy_dir.mkdir(parents=True, exist_ok=True)
        destination = _ensure_unique_path(legacy_dir / previous_legacy.name)
        shutil.move(str(previous_legacy), destination)

    marker_path.touch()


# ##################################################################
# format log timestamp
# returns a compact timestamp for log filenames
def _format_log_timestamp(timestamp: datetime) -> str:
    return timestamp.strftime("%y%m%d_%H%M%S")


# ##################################################################
# get process log dir
# returns the year/month directory for a process log
def _get_process_log_dir(name: str, timestamp: datetime) -> Path:
    log_dir = get_log_dir()
    return log_dir / name / timestamp.strftime("%Y") / timestamp.strftime("%m")


# ##################################################################
# ensure unique path
# avoids collisions when a file already exists
def _ensure_unique_path(path: Path) -> Path:
    if not path.exists():
        return path
    counter = 1
    while True:
        candidate = path.with_name(f"{path.stem}_{counter}{path.suffix}")
        if not candidate.exists():
            return candidate
        counter += 1


# ##################################################################
# get new log path
# returns a unique log path for the given process
def get_new_log_path(name: str) -> Path:
    migrate_legacy_logs()
    timestamp = datetime.now()
    log_dir = _get_process_log_dir(name, timestamp)
    log_dir.mkdir(parents=True, exist_ok=True)
    filename = f"{name}_{_format_log_timestamp(timestamp)}.log"
    return _ensure_unique_path(log_dir / filename)


# ##################################################################
# get latest log path
# returns the most recent log file path for a process
def get_latest_log_path(name: str) -> Optional[Path]:
    state = load_state()
    process_state = state.get(name)
    if isinstance(process_state, dict):
        log_path = process_state.get("log_path")
        if log_path:
            path = Path(log_path)
            if path.exists():
                return path

    process_root = get_log_dir() / name
    if not process_root.exists():
        return None
    candidates = [path for path in process_root.rglob("*.log") if path.is_file()]
    if not candidates:
        return None
    return max(candidates, key=lambda path: path.stat().st_mtime)


# ##################################################################
# load state file
# reads the entire state file from disk or returns empty dict with processes key
def _load_state_file() -> dict:
    state_path = get_state_path()
    if not state_path.exists():
        return {"processes": {}}
    with open(state_path, "r") as f:
        data = json.load(f)
        if "processes" not in data:
            data["processes"] = {}
        return data


# ##################################################################
# save state file
# writes the entire state file to disk
def _save_state_file(state_data: dict) -> None:
    state_path = get_state_path()
    state_path.parent.mkdir(parents=True, exist_ok=True)
    with open(state_path, "w") as f:
        json.dump(state_data, f, indent=2)


# ##################################################################
# load config
# reads process definitions from the processes section of state file or returns empty dict
def load_config() -> dict:
    state_data = _load_state_file()
    processes = state_data.get("processes", {})
    config = {}
    for name, data in processes.items():
        if not isinstance(data, dict):
            continue
        command = data.get("command")
        if command is None:
            continue
        config[name] = {
            "command": command,
            "port": data.get("port"),
            "workdir": data.get("workdir")
        }
    return config


# ##################################################################
# save config
# writes process definitions back to the processes section of state file preserving runtime state
def save_config(config: dict) -> None:
    state_data = _load_state_file()
    if "processes" not in state_data:
        state_data["processes"] = {}
    for name, command in config.items():
        if isinstance(command, dict):
            command_value = command.get("command")
            port_value = command.get("port")
            workdir_value = command.get("workdir")
        else:
            command_value = command
            port_value = None
            workdir_value = None

        if name not in state_data["processes"]:
            state_data["processes"][name] = {"command": command_value}
        elif isinstance(state_data["processes"][name], dict):
            state_data["processes"][name]["command"] = command_value
        else:
            state_data["processes"][name] = {"command": command_value, "pid": state_data["processes"][name]}

        if port_value is not None:
            state_data["processes"][name]["port"] = port_value
        else:
            state_data["processes"][name].pop("port", None)

        if workdir_value is not None:
            state_data["processes"][name]["workdir"] = workdir_value
        else:
            state_data["processes"][name].pop("workdir", None)
    _save_state_file(state_data)


# ##################################################################
# load state
# reads process runtime state including pids and stop flags from the processes section
def load_state() -> dict:
    state_data = _load_state_file()
    return state_data.get("processes", {})


# ##################################################################
# save state
# writes process runtime state back to the processes section preserving commands
def save_state(state: dict) -> None:
    state_data = _load_state_file()
    if "processes" not in state_data:
        state_data["processes"] = {}
    preserved_fields = ["command", "port", "workdir", "log_path"]
    for name, process_state in state.items():
        if name not in state_data["processes"]:
            state_data["processes"][name] = process_state
        elif isinstance(process_state, dict):
            existing = state_data["processes"][name]
            if isinstance(existing, dict):
                state_data["processes"][name] = process_state
                for field in preserved_fields:
                    if field in existing and field not in state_data["processes"][name]:
                        state_data["processes"][name][field] = existing[field]
            else:
                state_data["processes"][name] = process_state
        else:
            existing = state_data["processes"][name]
            if isinstance(existing, dict):
                state_data["processes"][name] = {"pid": process_state}
                for field in preserved_fields:
                    if field in existing:
                        state_data["processes"][name][field] = existing[field]
            else:
                state_data["processes"][name] = process_state
    _save_state_file(state_data)


# ##################################################################
# get process start time
# returns the start time string for a process or None if not found
def get_process_start_time(pid: int) -> Optional[str]:
    try:
        result = subprocess.run(
            ["ps", "-p", str(pid), "-o", "lstart="],
            capture_output=True,
            text=True
        )
        if result.returncode != 0:
            return None
        return result.stdout.strip() or None
    except Exception:
        return None


# ##################################################################
# is process alive
# checks if a process with the given pid is currently running and not a zombie
def is_process_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        # check if it's a zombie by reading /proc or using ps
        try:
            with open(f"/proc/{pid}/stat", "r") as f:
                stat = f.read()
                state = stat.split()[2]
                return state not in ("Z", "X")
        except FileNotFoundError:
            # proc filesystem not available on macos so use ps
            result = subprocess.run(
                ["ps", "-p", str(pid), "-o", "state="],
                capture_output=True,
                text=True
            )
            if result.returncode != 0:
                return False
            state = result.stdout.strip()
            return state not in ("Z", "Z+")
    except OSError:
        return False


# ##################################################################
# is our process
# checks if pid is alive AND matches the stored start time (handles PID reuse after reboot)
def is_our_process(pid: int, expected_start_time: Optional[str]) -> bool:
    if not is_process_alive(pid):
        return False
    if expected_start_time is None:
        # no start time stored - assume stale PID from before fix was deployed
        # return False to force restart and get proper start_time tracking
        return False
    actual_start_time = get_process_start_time(pid)
    if actual_start_time is None:
        return False
    return actual_start_time == expected_start_time


# ##################################################################
# is port free
# checks if a TCP port is available for binding
def is_port_free(port: int) -> bool:
    import socket
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        try:
            sock.bind(("127.0.0.1", port))
            return True
        except OSError:
            return False


# ##################################################################
# wait for port free
# polls until a port is free or timeout is reached
def wait_for_port_free(port: int, timeout_seconds: float = 10, poll_interval: float = 0.1) -> bool:
    elapsed = 0.0
    while elapsed < timeout_seconds:
        if is_port_free(port):
            return True
        time.sleep(poll_interval)
        elapsed += poll_interval
    return False


# ##################################################################
# is explicitly stopped
# checks if a process has been marked as explicitly stopped by the user
def is_explicitly_stopped(name: str) -> bool:
    state = load_state()
    process_state = state.get(name)
    if process_state is None:
        return False
    if isinstance(process_state, int):
        return False
    return process_state.get("explicitly_stopped", False)


# ##################################################################
# get process status
# returns the pid if the process is alive or none if it is dead or never started
def get_process_status(name: str) -> Optional[int]:
    state = load_state()
    process_state = state.get(name)
    if process_state is None:
        return None
    if isinstance(process_state, int):
        pid = process_state
        start_time = None
    else:
        pid = process_state.get("pid")
        start_time = process_state.get("start_time")
    if pid is None:
        return None
    if is_our_process(pid, start_time):
        return pid
    return None


# ##################################################################
# start process
# launches a process with the given name and redirects output to log files
def start_process(name: str) -> int:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")

    # check if already running
    current_pid = get_process_status(name)
    if current_pid is not None:
        raise RuntimeError(f"Process {name} is already running with pid {current_pid}")

    process_config = config[name]
    command = process_config["command"]
    workdir = process_config.get("workdir")
    log_path = get_new_log_path(name)

    # open log file
    log_file = open(log_path, "w")

    # start process with exec to replace shell with actual command
    wrapped_command = f"exec {command}"
    process = subprocess.Popen(
        wrapped_command,
        shell=True,
        stdout=log_file,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        cwd=workdir or None
    )
    log_file.close()

    # get the start time of the new process for identity verification
    start_time = get_process_start_time(process.pid)

    # update state, preserving existing restart_attempt and last_restart_time
    state = load_state()
    existing = state.get(name, {})
    if isinstance(existing, int):
        existing = {"pid": existing, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
    state[name] = {
        "pid": process.pid,
        "start_time": start_time,
        "explicitly_stopped": False,
        "restart_attempt": existing.get("restart_attempt", 0),
        "last_restart_time": existing.get("last_restart_time"),
        "log_path": str(log_path)
    }
    save_state(state)

    return process.pid


# ##################################################################
# wait for process death
# polls until a process with the given pid is no longer running with timeout
def wait_for_process_death(pid: int, timeout_seconds: float = 10, poll_interval: float = 0.1) -> bool:
    elapsed = 0.0
    while elapsed < timeout_seconds:
        if not is_process_alive(pid):
            return True
        time.sleep(poll_interval)
        elapsed += poll_interval
    return False


# ##################################################################
# stop process
# sends sigterm to a running process and marks it as explicitly stopped
def stop_process(name: str, mark_explicit: bool = True) -> None:
    pid = get_process_status(name)
    if pid is None:
        raise RuntimeError(f"Process {name} is not running")

    try:
        # kill the entire process group since we start with start_new_session=True
        # this ensures child processes (like uvicorn reloader/worker) are also terminated
        pgid = os.getpgid(pid)
        os.killpg(pgid, signal.SIGTERM)
    except OSError as err:
        raise RuntimeError(f"Failed to stop process {name} with pid {pid}: {err}")

    if mark_explicit:
        # mark as explicitly stopped
        state = load_state()
        if name in state:
            if isinstance(state[name], int):
                state[name] = {"pid": state[name], "explicitly_stopped": True}
            else:
                state[name]["explicitly_stopped"] = True
            save_state(state)


# ##################################################################
# shutdown all processes
# stops all running processes without marking them explicitly stopped
def shutdown_all_processes(timeout_seconds: int = 10) -> None:
    config = load_config()
    for name in config:
        pid = get_process_status(name)
        if pid is None:
            continue
        try:
            stop_process(name, mark_explicit=False)
            if not wait_for_process_death(pid, timeout_seconds=timeout_seconds):
                print(f"Warning: process {name} did not die within timeout")
        except Exception as err:
            print(f"Failed to stop {name}: {err}")


# ##################################################################
# add process
# adds a new process definition to config
def add_process(name: str, command: str, port: Optional[int] = None, workdir: Optional[str] = None) -> None:
    config = load_config()
    if name in config:
        raise ValueError(f"Process {name} already exists")

    if port is not None:
        try:
            port = int(port)
        except (TypeError, ValueError):
            raise ValueError("Port must be an integer")
        if port < 1 or port > 65535:
            raise ValueError("Port must be between 1 and 65535")

    if workdir is None:
        workdir = str(Path.cwd())
    else:
        workdir_path = Path(workdir).expanduser().resolve()
        if not workdir_path.is_dir():
            raise ValueError(f"Workdir does not exist: {workdir_path}")
        workdir = str(workdir_path)

    config[name] = {"command": command, "port": port, "workdir": workdir}
    save_config(config)


# ##################################################################
# update process
# updates an existing process definition in config
def update_process(name: str, port: Optional[int] = None, workdir: Optional[str] = None) -> None:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")

    if port is not None:
        try:
            port = int(port)
        except (TypeError, ValueError):
            raise ValueError("Port must be an integer")
        if port < 1 or port > 65535:
            raise ValueError("Port must be between 1 and 65535")
        config[name]["port"] = port

    if workdir is not None:
        workdir_path = Path(workdir).expanduser().resolve()
        if not workdir_path.is_dir():
            raise ValueError(f"Workdir does not exist: {workdir_path}")
        config[name]["workdir"] = str(workdir_path)

    save_config(config)


# ##################################################################
# remove process
# removes a process definition and all state from the state file and stops it if running
def remove_process(name: str) -> None:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")

    # stop if running
    pid = get_process_status(name)
    if pid is not None:
        stop_process(name)

    # remove entirely from state file
    state_data = _load_state_file()
    if "processes" in state_data and name in state_data["processes"]:
        del state_data["processes"][name]
        _save_state_file(state_data)


# ##################################################################
# list processes
# returns a dict mapping process names to their status including pid or none
def list_processes() -> dict:
    config = load_config()
    result = {}
    for name, definition in config.items():
        pid = get_process_status(name)
        result[name] = {
            "command": definition["command"],
            "pid": pid,
            "port": definition.get("port"),
            "workdir": definition.get("workdir")
        }
    return result


# ##################################################################
# get process command
# returns the command line for a given process name
def get_process_command(name: str) -> str:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")
    return config[name]["command"]


# ##################################################################
# get restart backoff seconds
# calculates the backoff time for a process based on its restart attempt count
def get_restart_backoff_seconds(name: str) -> int:
    state = load_state()
    process_state = state.get(name)
    if process_state is None or isinstance(process_state, int):
        return 1
    attempt = process_state.get("restart_attempt", 0)
    max_backoff = 2 * 60 * 60
    backoff = min(2 ** attempt, max_backoff)
    return backoff


# ##################################################################
# get last restart time
# returns the timestamp of the last restart attempt or none if never attempted
def get_last_restart_time(name: str) -> Optional[float]:
    state = load_state()
    process_state = state.get(name)
    if process_state is None or isinstance(process_state, int):
        return None
    return process_state.get("last_restart_time")


# ##################################################################
# increment restart attempt
# increments the restart attempt counter and updates the last restart time
def increment_restart_attempt(name: str) -> None:
    state = load_state()
    process_state = state.get(name)
    if process_state is None:
        process_state = {"pid": None, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
        state[name] = process_state
    elif isinstance(process_state, int):
        pid = process_state
        process_state = {"pid": pid, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
        state[name] = process_state
    process_state["restart_attempt"] = process_state.get("restart_attempt", 0) + 1
    process_state["last_restart_time"] = time.time()
    save_state(state)


# ##################################################################
# reset restart attempt
# resets the restart attempt counter when a process starts successfully
def reset_restart_attempt(name: str) -> None:
    state = load_state()
    process_state = state.get(name)
    if process_state is not None and isinstance(process_state, dict):
        process_state["restart_attempt"] = 0
        process_state["last_restart_time"] = None
        save_state(state)


# ##################################################################
# should restart process
# determines if a process should be restarted based on its state and backoff
def should_restart_process(name: str) -> bool:
    if is_explicitly_stopped(name):
        return False
    pid = get_process_status(name)
    if pid is not None:
        return False
    last_restart = get_last_restart_time(name)
    if last_restart is None:
        return True
    backoff = get_restart_backoff_seconds(name)
    elapsed = time.time() - last_restart
    return elapsed >= backoff


# ##################################################################
# watch and restart processes
# monitors all configured processes and restarts those that have died unexpectedly
def watch_and_restart_processes() -> None:
    config = load_config()
    for name in config:
        if not should_restart_process(name):
            continue
        try:
            increment_restart_attempt(name)
            pid = start_process(name)
            backoff = get_restart_backoff_seconds(name)
            print(f"Restarted {name} with pid {pid} after {backoff}s backoff")
        except Exception as err:
            print(f"Failed to restart {name}: {err}")


# ##################################################################
# start all processes
# starts all configured processes that are not currently running
def start_all_processes() -> None:
    config = load_config()
    for name in config:
        pid = get_process_status(name)
        if pid is None:
            try:
                start_process(name)
            except Exception as err:
                # log but continue with other processes
                print(f"Failed to start {name}: {err}")


# ##################################################################
# get launch agent path
# returns the path where the launch agent plist should be installed
def get_launch_agent_path() -> Path:
    home = Path.home()
    return home / "Library" / "LaunchAgents" / "com.darrenoakey.auto.plist"


# ##################################################################
# get plist template path
# returns the path to the plist template in the project
def get_plist_template_path() -> Path:
    return get_project_root() / "com.darrenoakey.auto.plist"


# ##################################################################
# generate plist content
# creates plist xml with paths dynamically determined from current project location
def _generate_plist_content() -> str:
    project_root = get_project_root()
    run_script = project_root / "run"
    auto_log_path = get_auto_log_path()

    # get current PATH and LANG from environment
    current_path = os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
    current_lang = os.environ.get("LANG", "en_US.UTF-8")

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
    <string>{auto_log_path}</string>
    <key>StandardErrorPath</key>
    <string>{auto_log_path}</string>
    <key>KeepAlive</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{current_path}</string>
        <key>LANG</key>
        <string>{current_lang}</string>
    </dict>
</dict>
</plist>
"""
    return plist_content


# ##################################################################
# install launch agent
# generates plist with dynamic paths and installs it to launch agents directory
def install_launch_agent() -> None:
    launch_agent_path = get_launch_agent_path()
    launch_agent_path.parent.mkdir(parents=True, exist_ok=True)

    # unload if already loaded
    if launch_agent_path.exists():
        try:
            subprocess.run(
                ["launchctl", "unload", str(launch_agent_path)],
                capture_output=True,
                check=False
            )
        except Exception:
            pass

    # generate and write plist file with dynamic paths
    plist_content = _generate_plist_content()
    with open(launch_agent_path, "w") as f:
        f.write(plist_content)

    # load the launch agent
    result = subprocess.run(
        ["launchctl", "load", str(launch_agent_path)],
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        raise RuntimeError(f"Failed to load launch agent: {result.stderr}")


# ##################################################################
# uninstall launch agent
# unloads and removes the plist file from launch agents directory
def uninstall_launch_agent() -> None:
    launch_agent_path = get_launch_agent_path()

    if not launch_agent_path.exists():
        raise FileNotFoundError(f"Launch agent not installed at {launch_agent_path}")

    # unload the launch agent
    result = subprocess.run(
        ["launchctl", "unload", str(launch_agent_path)],
        capture_output=True,
        text=True
    )

    if result.returncode != 0:
        raise RuntimeError(f"Failed to unload launch agent: {result.stderr}")

    # remove plist file
    launch_agent_path.unlink()


# ##################################################################
# get wrapper script path
# returns the path where the auto wrapper script should be installed
def get_wrapper_script_path() -> Path:
    home = Path.home()
    return home / "bin" / "auto"


# ##################################################################
# generate wrapper script content
# creates bash wrapper that delegates to the run script in current project
def _generate_wrapper_script_content() -> str:
    project_root = get_project_root()
    run_script = project_root / "run"

    wrapper_content = f"""#!/bin/bash
# ##################################################################
# auto wrapper
# delegates all commands to the run script in the auto project directory
exec {run_script} "$@"
"""
    return wrapper_content


# ##################################################################
# install wrapper script
# generates and installs the auto wrapper script to user bin directory
def install_wrapper_script() -> None:
    wrapper_path = get_wrapper_script_path()
    wrapper_path.parent.mkdir(parents=True, exist_ok=True)

    # generate and write wrapper script with dynamic path
    wrapper_content = _generate_wrapper_script_content()
    with open(wrapper_path, "w") as f:
        f.write(wrapper_content)

    # make executable
    wrapper_path.chmod(0o755)


# ##################################################################
# uninstall wrapper script
# removes the auto wrapper script from user bin directory
def uninstall_wrapper_script() -> None:
    wrapper_path = get_wrapper_script_path()

    if not wrapper_path.exists():
        raise FileNotFoundError(f"Wrapper script not installed at {wrapper_path}")

    wrapper_path.unlink()
