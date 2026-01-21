#!/usr/bin/env python3
import os
import signal
import time
from datetime import datetime
from pathlib import Path

import pytest

import daemon_manager as dm


# ##################################################################
# test fixture temp dir
# creates a temporary isolated environment for each test
@pytest.fixture
def temp_dir(tmp_path, monkeypatch):
    project_root = tmp_path / "test_project"
    project_root.mkdir()
    local_dir = project_root / "local"
    local_dir.mkdir()
    output_dir = project_root / "output"
    output_dir.mkdir()

    def get_test_root():
        return project_root

    monkeypatch.setattr(dm, "get_project_root", get_test_root)
    monkeypatch.chdir(project_root)
    return project_root


# ##################################################################
# test add and list process
# ensures we can add a process and see it in the list
def test_add_and_list_process(temp_dir):
    dm.add_process("test1", "echo hello", port=8080)
    processes = dm.list_processes()
    assert "test1" in processes
    assert processes["test1"]["command"] == "echo hello"
    assert processes["test1"]["pid"] is None
    assert processes["test1"]["port"] == 8080
    assert processes["test1"]["workdir"] == str(temp_dir)


# ##################################################################
# test start process
# ensures we can start a process and it receives a pid
def test_start_process(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    pid = dm.start_process("sleeper")
    assert pid is not None
    assert dm.is_process_alive(pid)
    status_pid = dm.get_process_status("sleeper")
    assert status_pid == pid
    os.kill(pid, signal.SIGTERM)


# ##################################################################
# test stop process marks explicitly stopped
# ensures stopping a process sets the explicitly stopped flag
def test_stop_process_marks_explicitly_stopped(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    dm.start_process("sleeper")
    assert not dm.is_explicitly_stopped("sleeper")
    dm.stop_process("sleeper")
    assert dm.is_explicitly_stopped("sleeper")


# ##################################################################
# test shutdown does not mark explicitly stopped
# ensures shutdown stops processes without setting explicitly stopped
def test_shutdown_does_not_mark_explicitly_stopped(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    pid = dm.start_process("sleeper")
    dm.shutdown_all_processes(timeout_seconds=2)
    assert not dm.is_explicitly_stopped("sleeper")
    assert dm.wait_for_process_death(pid, timeout_seconds=2)


# ##################################################################
# test should restart process when dead and not explicitly stopped
# ensures we restart processes that die unexpectedly
def test_should_restart_process_when_dead_and_not_explicitly_stopped(temp_dir):
    dm.add_process("test", "sleep 100")
    state = dm.load_state()
    state["test"] = {"pid": 99999, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
    dm.save_state(state)
    assert dm.should_restart_process("test")


# ##################################################################
# test should not restart explicitly stopped process
# ensures we never restart a process the user stopped
def test_should_not_restart_explicitly_stopped_process(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    dm.start_process("sleeper")
    dm.stop_process("sleeper")
    time.sleep(0.1)
    assert not dm.should_restart_process("sleeper")


# ##################################################################
# test exponential backoff increases
# ensures backoff time doubles with each restart attempt
def test_exponential_backoff_increases(temp_dir):
    dm.add_process("test", "sleep 100")
    state = dm.load_state()
    state["test"] = {"pid": None, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
    dm.save_state(state)
    backoff0 = dm.get_restart_backoff_seconds("test")
    assert backoff0 == 1

    state["test"]["restart_attempt"] = 1
    dm.save_state(state)
    backoff1 = dm.get_restart_backoff_seconds("test")
    assert backoff1 == 2

    state["test"]["restart_attempt"] = 5
    dm.save_state(state)
    backoff5 = dm.get_restart_backoff_seconds("test")
    assert backoff5 == 32


# ##################################################################
# test backoff caps at max
# ensures backoff never exceeds two hours
def test_backoff_caps_at_max(temp_dir):
    dm.add_process("test", "sleep 100")
    state = dm.load_state()
    state["test"] = {"pid": None, "explicitly_stopped": False, "restart_attempt": 100, "last_restart_time": None}
    dm.save_state(state)
    backoff = dm.get_restart_backoff_seconds("test")
    assert backoff == 2 * 60 * 60


# ##################################################################
# test should not restart before backoff elapsed
# ensures we respect the backoff period
def test_should_not_restart_before_backoff_elapsed(temp_dir):
    dm.add_process("test", "sleep 100")
    state = dm.load_state()
    state["test"] = {"pid": None, "explicitly_stopped": False, "restart_attempt": 2, "last_restart_time": time.time()}
    dm.save_state(state)
    assert not dm.should_restart_process("test")


# ##################################################################
# test should restart after backoff elapsed
# ensures we restart once the backoff period has passed
def test_should_restart_after_backoff_elapsed(temp_dir):
    dm.add_process("test", "sleep 100")
    past_time = time.time() - 10
    state = dm.load_state()
    state["test"] = {"pid": None, "explicitly_stopped": False, "restart_attempt": 2, "last_restart_time": past_time}
    dm.save_state(state)
    assert dm.should_restart_process("test")


# ##################################################################
# test watch and restart processes
# ensures watch loop restarts dead processes
def test_watch_and_restart_processes(temp_dir):
    dm.add_process("test", "sleep 100")
    state = dm.load_state()
    state["test"] = {"pid": 99999, "explicitly_stopped": False, "restart_attempt": 0, "last_restart_time": None}
    dm.save_state(state)
    dm.watch_and_restart_processes()
    pid = dm.get_process_status("test")
    assert pid is not None
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test remove process
# ensures removing a process stops it and removes config
def test_remove_process(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    dm.start_process("sleeper")
    dm.remove_process("sleeper")
    processes = dm.list_processes()
    assert "sleeper" not in processes


# ##################################################################
# test backward compatibility with old state format
# ensures old integer pid format still works
def test_backward_compatibility_with_old_state_format(temp_dir):
    dm.add_process("test", "sleep 100")
    pid = dm.start_process("test")
    state = dm.load_state()
    state["test"] = pid
    dm.save_state(state)
    assert dm.get_process_status("test") == pid
    assert not dm.is_explicitly_stopped("test")
    os.kill(pid, signal.SIGTERM)


# ##################################################################
# test wait for process death succeeds when process dies
# ensures wait_for_process_death returns true when process terminates
def test_wait_for_process_death_succeeds_when_process_dies(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    pid = dm.start_process("sleeper")
    time.sleep(0.1)
    assert dm.is_process_alive(pid)
    os.kill(pid, signal.SIGKILL)
    assert dm.wait_for_process_death(pid, timeout_seconds=5)
    assert not dm.is_process_alive(pid)


# ##################################################################
# test wait for process death times out when process doesnt die
# ensures wait_for_process_death returns false when timeout exceeded
def test_wait_for_process_death_times_out_when_process_doesnt_die(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    pid = dm.start_process("sleeper")
    assert dm.is_process_alive(pid)
    result = dm.wait_for_process_death(pid, timeout_seconds=0.5)
    assert not result
    assert dm.is_process_alive(pid)
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test start process creates timestamped log
# ensures logs are created under process/year/month with timestamped filename
def test_start_process_creates_timestamped_log(temp_dir):
    now = datetime.now()
    dm.add_process("logger", "sleep 100")
    pid = dm.start_process("logger")
    log_path = dm.get_latest_log_path("logger")
    assert log_path is not None
    assert log_path.exists()
    assert log_path.name.startswith("logger_")
    assert log_path.suffix == ".log"
    expected_dir = dm.get_log_dir() / "logger" / now.strftime("%Y") / now.strftime("%m")
    assert log_path.parent == expected_dir
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test migrate legacy logs moves root files
# ensures old flat log files get archived under _legacy
def test_migrate_legacy_logs_moves_root_files(temp_dir):
    log_dir = dm.get_log_dir()
    legacy_file = log_dir / "old.stdout.log"
    legacy_file.write_text("legacy")

    dm.migrate_legacy_logs()

    assert not legacy_file.exists()
    legacy_root = log_dir / "old"
    archived = list(legacy_root.rglob("old.stdout.log"))
    assert archived


# ##################################################################
# test update process port
# ensures we can update the port of an existing process
def test_update_process_port(temp_dir):
    dm.add_process("test", "echo hello", port=8080)
    processes = dm.list_processes()
    assert processes["test"]["port"] == 8080

    dm.update_process("test", port=9090)
    processes = dm.list_processes()
    assert processes["test"]["port"] == 9090


# ##################################################################
# test update process workdir
# ensures we can update the workdir of an existing process
def test_update_process_workdir(temp_dir):
    dm.add_process("test", "echo hello")
    new_dir = temp_dir / "subdir"
    new_dir.mkdir()

    dm.update_process("test", workdir=str(new_dir))
    processes = dm.list_processes()
    assert processes["test"]["workdir"] == str(new_dir)


# ##################################################################
# test update process nonexistent raises
# ensures updating a non-existent process raises an error
def test_update_process_nonexistent_raises(temp_dir):
    with pytest.raises(ValueError, match="not found"):
        dm.update_process("nonexistent", port=8080)


# ##################################################################
# test is port free returns true for unused port
# ensures is_port_free correctly detects available ports
def test_is_port_free_returns_true_for_unused_port(temp_dir):
    import socket
    # find an unused port
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    # port should be free now that socket is closed
    assert dm.is_port_free(port)


# ##################################################################
# test is port free returns false for used port
# ensures is_port_free correctly detects ports in use
def test_is_port_free_returns_false_for_used_port(temp_dir):
    import socket
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    try:
        assert not dm.is_port_free(port)
    finally:
        sock.close()


# ##################################################################
# test wait for port free succeeds when port freed
# ensures wait_for_port_free returns true when port becomes available
def test_wait_for_port_free_succeeds_when_port_freed(temp_dir):
    import socket
    import threading
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]

    def release_port():
        time.sleep(0.2)
        sock.close()

    thread = threading.Thread(target=release_port)
    thread.start()
    result = dm.wait_for_port_free(port, timeout_seconds=2)
    thread.join()
    assert result


# ##################################################################
# test wait for port free times out when port busy
# ensures wait_for_port_free returns false when timeout exceeded
def test_wait_for_port_free_times_out_when_port_busy(temp_dir):
    import socket
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    try:
        result = dm.wait_for_port_free(port, timeout_seconds=0.3)
        assert not result
    finally:
        sock.close()
