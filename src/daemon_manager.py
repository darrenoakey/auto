#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import time
from pathlib import Path
from typing import Optional


# ##################################################################
# get project root
# finds the absolute path to the auto project directory
def get_project_root() -> Path:
    return Path(__file__).parent.parent.absolute()


# ##################################################################
# get config path
# returns the path to the config file that stores process definitions
def get_config_path() -> Path:
    return get_project_root() / "local" / "config.json"


# ##################################################################
# get state path
# returns the path to the state file that stores running process pids
def get_state_path() -> Path:
    return get_project_root() / "state.json"


# ##################################################################
# get log dir
# returns the directory where process logs are stored
def get_log_dir() -> Path:
    log_dir = get_project_root() / "output" / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir


# ##################################################################
# load config
# reads process definitions from disk or returns empty dict if file does not exist
def load_config() -> dict:
    config_path = get_config_path()
    if not config_path.exists():
        return {}
    with open(config_path, "r") as f:
        return json.load(f)


# ##################################################################
# save config
# writes process definitions to disk
def save_config(config: dict) -> None:
    config_path = get_config_path()
    with open(config_path, "w") as f:
        json.dump(config, f, indent=2)


# ##################################################################
# load state
# reads process runtime state including pids and stop flags from disk or returns empty dict
def load_state() -> dict:
    state_path = get_state_path()
    if not state_path.exists():
        return {}
    with open(state_path, "r") as f:
        return json.load(f)


# ##################################################################
# save state
# writes process runtime state to disk
def save_state(state: dict) -> None:
    state_path = get_state_path()
    with open(state_path, "w") as f:
        json.dump(state, f, indent=2)


# ##################################################################
# is process alive
# checks if a process with the given pid is currently running
def is_process_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except OSError:
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
    else:
        pid = process_state.get("pid")
    if pid is None:
        return None
    if is_process_alive(pid):
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

    command = config[name]
    log_dir = get_log_dir()
    stdout_log = log_dir / f"{name}.stdout.log"
    stderr_log = log_dir / f"{name}.stderr.log"

    # open log files
    stdout_file = open(stdout_log, "a")
    stderr_file = open(stderr_log, "a")

    # start process with exec to replace shell with actual command
    wrapped_command = f"exec {command}"
    process = subprocess.Popen(
        wrapped_command,
        shell=True,
        stdout=stdout_file,
        stderr=stderr_file,
        start_new_session=True
    )

    # update state
    state = load_state()
    state[name] = {"pid": process.pid, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
    save_state(state)

    return process.pid


# ##################################################################
# stop process
# sends sigterm to a running process and marks it as explicitly stopped
def stop_process(name: str) -> None:
    pid = get_process_status(name)
    if pid is None:
        raise RuntimeError(f"Process {name} is not running")

    try:
        os.kill(pid, signal.SIGTERM)
    except OSError as err:
        raise RuntimeError(f"Failed to stop process {name} with pid {pid}: {err}")

    # mark as explicitly stopped
    state = load_state()
    if name in state:
        if isinstance(state[name], int):
            state[name] = {"pid": state[name], "explicitly_stopped": True}
        else:
            state[name]["explicitly_stopped"] = True
        save_state(state)


# ##################################################################
# add process
# adds a new process definition to config
def add_process(name: str, command: str) -> None:
    config = load_config()
    if name in config:
        raise ValueError(f"Process {name} already exists")
    config[name] = command
    save_config(config)


# ##################################################################
# remove process
# removes a process definition from config and stops it if running
def remove_process(name: str) -> None:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")

    # stop if running
    pid = get_process_status(name)
    if pid is not None:
        stop_process(name)

    # remove from config
    del config[name]
    save_config(config)


# ##################################################################
# list processes
# returns a dict mapping process names to their status including pid or none
def list_processes() -> dict:
    config = load_config()
    result = {}
    for name in config:
        pid = get_process_status(name)
        result[name] = {"command": config[name], "pid": pid}
    return result


# ##################################################################
# get process command
# returns the command line for a given process name
def get_process_command(name: str) -> str:
    config = load_config()
    if name not in config:
        raise ValueError(f"Process {name} not found in config")
    return config[name]


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
