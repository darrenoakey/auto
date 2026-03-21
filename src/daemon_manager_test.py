#!/usr/bin/env python3
import os
import signal
import socket
import subprocess
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
# ensures old integer pid format is treated as stale to force restart with proper tracking
def test_backward_compatibility_with_old_state_format(temp_dir):
    dm.add_process("test", "sleep 100")
    pid = dm.start_process("test")
    state = dm.load_state()
    state["test"] = pid  # old format: just pid, no start_time
    dm.save_state(state)
    # old-style entries without start_time are treated as stale for PID reuse safety
    assert dm.get_process_status("test") is None
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
# test update process command
# ensures we can update the command of an existing process
def test_update_process_command(temp_dir):
    dm.add_process("test", "echo hello")
    assert dm.get_process_command("test") == "echo hello"

    dm.update_process("test", command="echo goodbye")
    assert dm.get_process_command("test") == "echo goodbye"


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


# ##################################################################
# test parse lstart time handles both locale formats
# ensures we correctly parse US and AU/UK date formats from ps lstart
def test_parse_lstart_time_handles_locale_formats(temp_dir):
    # US format: "Mon Jan 26 10:35:12 2026"
    us_time = dm._parse_lstart_time("Mon Jan 26 10:35:12 2026")
    assert us_time is not None
    assert us_time.day == 26
    assert us_time.month == 1
    assert us_time.year == 2026
    assert us_time.hour == 10
    assert us_time.minute == 35
    assert us_time.second == 12

    # AU/UK format: "Mon 26 Jan 10:35:12 2026"
    au_time = dm._parse_lstart_time("Mon 26 Jan 10:35:12 2026")
    assert au_time is not None
    assert au_time.day == 26
    assert au_time.month == 1
    assert au_time.year == 2026

    # Both should parse to the same datetime
    assert us_time == au_time


# ##################################################################
# test is our process handles mismatched locale formats
# ensures processes are recognized even with different date format in state vs ps output
def test_is_our_process_handles_mismatched_locale_formats(temp_dir):
    dm.add_process("test", "sleep 100")
    pid = dm.start_process("test")

    # get actual start time from ps
    actual_start_time = dm.get_process_start_time(pid)
    assert actual_start_time is not None

    # convert to other locale format and verify it still matches
    parsed = dm._parse_lstart_time(actual_start_time)
    assert parsed is not None

    # try US format
    us_format = parsed.strftime("%a %b %d %H:%M:%S %Y")
    # try AU format
    au_format = parsed.strftime("%a %d %b %H:%M:%S %Y")

    # regardless of which format ps returned, both formatted versions should work
    assert dm.is_our_process(pid, us_format)
    assert dm.is_our_process(pid, au_format)

    os.kill(pid, signal.SIGTERM)


# ##################################################################
# test list processes includes explicitly stopped flag
# ensures list_processes returns explicitly_stopped for each process
def test_list_processes_includes_explicitly_stopped(temp_dir):
    dm.add_process("sleeper", "sleep 100")
    pid = dm.start_process("sleeper")

    # running process should not be explicitly stopped
    processes = dm.list_processes()
    assert processes["sleeper"]["explicitly_stopped"] is False

    # stop process and verify flag changes
    dm.stop_process("sleeper")
    dm.wait_for_process_death(pid, timeout_seconds=2)
    processes = dm.list_processes()
    assert processes["sleeper"]["explicitly_stopped"] is True
    assert processes["sleeper"]["pid"] is None


# ##################################################################
# test stop process escalates to sigkill
# ensures processes that ignore SIGTERM get killed with SIGKILL
def test_stop_process_escalates_to_sigkill(temp_dir):
    # create a script that traps SIGTERM but responds to SIGKILL
    script_path = temp_dir / "trap_sigterm.sh"
    script_path.write_text("#!/bin/bash\ntrap '' TERM\nsleep 999\n")
    script_path.chmod(0o755)

    dm.add_process("stubborn", str(script_path))
    pid = dm.start_process("stubborn")
    time.sleep(0.2)
    assert dm.is_process_alive(pid)

    # stop should escalate to SIGKILL and succeed
    dm.stop_process("stubborn")
    assert not dm.is_process_alive(pid)


# ##################################################################
# test start process force-frees port before starting
# ensures start_process kills port holders and succeeds instead of failing
def test_start_process_force_frees_port_before_starting(temp_dir):
    import socket
    # get a free port
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()

    # spawn a separate process that holds the port (so kill_port_holders can kill it)
    holder = subprocess.Popen(
        ["python3", "-c",
         f"import socket, time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(('127.0.0.1', {port})); s.listen(1); time.sleep(300)"],
        start_new_session=True
    )
    time.sleep(0.5)
    assert not dm.is_port_free(port)

    dm.add_process("test", "sleep 100", port=port)
    # start_process should kill the holder and succeed
    pid = dm.start_process("test")
    assert pid is not None
    assert dm.is_process_alive(pid)

    # clean up
    dm.stop_process("test")
    try:
        holder.kill()
        holder.wait(timeout=2)
    except Exception:
        pass


# ##################################################################
# test shutdown for reboot stops processes without marking explicitly stopped
# ensures shutdown_for_reboot kills all processes but leaves them eligible for restart after reboot
def test_shutdown_for_reboot_stops_processes_without_marking(temp_dir):
    dm.add_process("sleeper1", "sleep 100")
    dm.add_process("sleeper2", "sleep 100")
    pid1 = dm.start_process("sleeper1")
    pid2 = dm.start_process("sleeper2")

    assert dm.is_process_alive(pid1)
    assert dm.is_process_alive(pid2)

    # shutdown_for_reboot also calls launchctl bootout which will fail silently in test
    dm.shutdown_for_reboot()

    assert dm.wait_for_process_death(pid1, timeout_seconds=5)
    assert dm.wait_for_process_death(pid2, timeout_seconds=5)

    # must NOT be marked as explicitly stopped - they must restart after reboot
    assert not dm.is_explicitly_stopped("sleeper1")
    assert not dm.is_explicitly_stopped("sleeper2")


# ##################################################################
# test shutdown all processes kills in parallel not sequentially
# ensures multiple processes are killed concurrently (total time < sum of individual timeouts)
def test_shutdown_all_processes_kills_in_parallel(temp_dir):
    # start several processes
    for i in range(5):
        dm.add_process(f"par{i}", "sleep 100")
        dm.start_process(f"par{i}")

    start = time.monotonic()
    dm.shutdown_all_processes()
    elapsed = time.monotonic() - start

    # if sequential with 5s SIGTERM timeout each, this would take 25s+
    # parallel should complete well under 10s
    assert elapsed < 10, f"shutdown_all_processes took {elapsed:.1f}s — not parallel?"

    # all should be dead
    for i in range(5):
        assert dm.get_process_status(f"par{i}") is None


# ##################################################################
# test shutdown for reboot kills daemon before processes
# ensures the watch daemon is bootout'd before process killing begins
def test_shutdown_for_reboot_kills_daemon_first(temp_dir, monkeypatch):
    call_order = []

    original_shutdown_all = dm.shutdown_all_processes
    original_subprocess_run = subprocess.run

    def track_subprocess_run(cmd, *args, **kwargs):
        if isinstance(cmd, list) and "launchctl" in cmd:
            call_order.append("bootout")
            return subprocess.CompletedProcess(args=cmd, returncode=0)
        return original_subprocess_run(cmd, *args, **kwargs)

    def track_shutdown_all(*args, **kwargs):
        call_order.append("shutdown_all")
        original_shutdown_all(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", track_subprocess_run)
    monkeypatch.setattr(dm, "shutdown_all_processes", track_shutdown_all)
    monkeypatch.setattr(dm, "get_auto_daemon_pid", lambda: None)

    dm.add_process("sleeper", "sleep 100")
    dm.start_process("sleeper")

    dm.shutdown_for_reboot()

    assert call_order.index("bootout") < call_order.index("shutdown_all")


# ##################################################################
# test backoff resets after successful run
# ensures restart backoff is reset after process runs successfully for threshold time
def test_backoff_resets_after_successful_run(temp_dir):
    dm.add_process("test", "sleep 100")
    pid = dm.start_process("test")

    # simulate a restart with backoff
    state = dm.load_state()
    state["test"]["restart_attempt"] = 3
    state["test"]["last_restart_time"] = time.time() - 61  # 61 seconds ago
    dm.save_state(state)

    # verify backoff is accumulated
    assert dm.get_restart_backoff_seconds("test") == 8

    # call check_and_reset_backoff - should reset because process has been running > 60s
    dm.check_and_reset_backoff("test")

    # verify backoff is reset
    assert dm.get_restart_backoff_seconds("test") == 1
    state = dm.load_state()
    assert state["test"]["restart_attempt"] == 0
    assert state["test"]["last_restart_time"] is None

    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test kill port holders kills process holding a port
# ensures kill_port_holders finds and kills processes via lsof
def test_kill_port_holders_kills_process_on_port(temp_dir):
    # get a free port
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()

    # start a subprocess in its own session so killpg doesn't hit the test runner
    proc = subprocess.Popen(
        ["python3", "-c", f"import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',{port})); s.listen(1); time.sleep(100)"],
        start_new_session=True,
    )
    time.sleep(0.3)

    assert not dm.is_port_free(port)
    killed = dm.kill_port_holders(port)
    assert len(killed) > 0
    dm.wait_for_port_free(port, timeout_seconds=2)
    assert dm.is_port_free(port)
    proc.wait(timeout=2)


# ##################################################################
# test kill port holders returns empty when port is free
# ensures kill_port_holders is a no-op for unused ports
def test_kill_port_holders_returns_empty_for_free_port(temp_dir):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    killed = dm.kill_port_holders(port)
    assert killed == []


# ##################################################################
# test force free port kills holders and waits
# ensures force_free_port returns True after killing port occupants
def test_force_free_port_succeeds(temp_dir):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()

    proc = subprocess.Popen(
        ["python3", "-c", f"import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',{port})); s.listen(1); time.sleep(100)"],
        start_new_session=True,
    )
    time.sleep(0.3)

    assert not dm.is_port_free(port)
    result = dm.force_free_port(port)
    assert result
    assert dm.is_port_free(port)
    proc.wait(timeout=2)


# ##################################################################
# test force free port returns true when already free
# ensures force_free_port is a fast no-op for available ports
def test_force_free_port_returns_true_when_already_free(temp_dir):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    assert dm.force_free_port(port)


# ##################################################################
# test stop process frees port from orphaned children
# ensures stop_process kills orphaned children holding the port
def test_stop_process_frees_port_from_orphaned_children(temp_dir):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()

    script = temp_dir / "spawner.sh"
    script.write_text(f"""#!/bin/bash
python3 -c "import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',{port})); s.listen(1); time.sleep(100)" &
sleep 100
""")
    script.chmod(0o755)

    dm.add_process("spawner", str(script), port=port)
    pid = dm.start_process("spawner")
    # wait for the background child to actually bind the port (python startup is slow)
    deadline = time.time() + 3
    while dm.is_port_free(port) and time.time() < deadline:
        time.sleep(0.1)
    assert not dm.is_port_free(port), f"Child never bound port {port}"
    dm.stop_process("spawner")
    assert dm.is_port_free(port)


# ##################################################################
# test restart dead processes restarts dead but not stopped or running
# ensures restart_dead_processes only restarts processes that died unexpectedly
def test_restart_dead_processes(temp_dir):
    dm.add_process("alive", "sleep 100")
    dm.add_process("dead", "sleep 100")
    dm.add_process("stopped", "sleep 100")

    # start all three
    pid_alive = dm.start_process("alive")
    pid_dead = dm.start_process("dead")
    pid_stopped = dm.start_process("stopped")

    # kill "dead" without marking explicitly stopped
    os.kill(pid_dead, signal.SIGKILL)
    dm.wait_for_process_death(pid_dead, timeout_seconds=2)

    # stop "stopped" explicitly
    dm.stop_process("stopped")

    results = dm.restart_dead_processes()

    # only "dead" should have been restarted
    assert "dead" in results
    assert isinstance(results["dead"], int)
    assert "alive" not in results
    assert "stopped" not in results

    # verify "dead" is now running again
    new_pid = dm.get_process_status("dead")
    assert new_pid is not None
    assert new_pid != pid_dead

    # cleanup
    os.kill(pid_alive, signal.SIGKILL)
    os.kill(new_pid, signal.SIGKILL)


# ##################################################################
# test parse interval
# ensures various human-readable interval strings are parsed correctly
def test_parse_interval():
    assert dm.parse_interval("30m") == 1800
    assert dm.parse_interval("24h") == 86400
    assert dm.parse_interval("1d") == 86400
    assert dm.parse_interval("7d") == 604800
    assert dm.parse_interval("90s") == 90
    assert dm.parse_interval("3600") == 3600
    assert dm.parse_interval("0.5h") == 1800
    with pytest.raises(ValueError):
        dm.parse_interval("")
    with pytest.raises(ValueError):
        dm.parse_interval("abc")


# ##################################################################
# test format interval
# ensures seconds are formatted back to human-readable strings
def test_format_interval():
    assert dm.format_interval(86400) == "1d"
    assert dm.format_interval(172800) == "2d"
    assert dm.format_interval(3600) == "1h"
    assert dm.format_interval(7200) == "2h"
    assert dm.format_interval(1800) == "30m"
    assert dm.format_interval(60) == "1m"
    assert dm.format_interval(45) == "45s"
    assert dm.format_interval(90) == "90s"


# ##################################################################
# test set and get restart interval
# ensures restart interval can be stored and retrieved for a process
def test_set_and_get_restart_interval(temp_dir):
    dm.add_process("test1", "sleep 100")
    assert dm.get_restart_interval("test1") is None
    dm.set_restart_interval("test1", 86400)
    assert dm.get_restart_interval("test1") == 86400
    # clear it
    dm.set_restart_interval("test1", None)
    assert dm.get_restart_interval("test1") is None


# ##################################################################
# test needs periodic restart false when no interval set
# ensures processes without a restart interval never trigger periodic restart
def test_needs_periodic_restart_no_interval(temp_dir):
    dm.add_process("test1", "sleep 100")
    pid = dm.start_process("test1")
    assert not dm.needs_periodic_restart("test1")
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test needs periodic restart true when interval elapsed
# ensures periodic restart triggers after the configured interval
def test_needs_periodic_restart_elapsed(temp_dir):
    dm.add_process("test1", "sleep 100")
    pid = dm.start_process("test1")
    dm.set_restart_interval("test1", 60)
    # set last_periodic_restart to 120 seconds ago
    state = dm.load_state()
    state["test1"]["last_periodic_restart"] = time.time() - 120
    dm.save_state(state)
    assert dm.needs_periodic_restart("test1")
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test needs periodic restart false when interval not elapsed
# ensures periodic restart does not trigger before the interval
def test_needs_periodic_restart_not_elapsed(temp_dir):
    dm.add_process("test1", "sleep 100")
    pid = dm.start_process("test1")
    dm.set_restart_interval("test1", 86400)
    # last_periodic_restart is set to now by set_restart_interval
    assert not dm.needs_periodic_restart("test1")
    os.kill(pid, signal.SIGKILL)


# ##################################################################
# test perform periodic restart
# ensures a running process is restarted and timestamp is updated
def test_perform_periodic_restart(temp_dir):
    dm.add_process("test1", "sleep 100")
    old_pid = dm.start_process("test1")
    dm.set_restart_interval("test1", 60)
    before = time.time()
    new_pid = dm.perform_periodic_restart("test1")
    assert new_pid is not None
    assert new_pid != old_pid
    # old pid should be dead
    assert not dm.is_process_alive(old_pid)
    # new pid should be alive
    assert dm.is_process_alive(new_pid)
    # last_periodic_restart should be updated
    state = dm.load_state()
    assert state["test1"]["last_periodic_restart"] >= before
    # cleanup
    os.kill(new_pid, signal.SIGKILL)


# ##################################################################
# test restart interval preserved across start
# ensures the restart interval survives a process restart
def test_restart_interval_preserved_across_start(temp_dir):
    dm.add_process("test1", "sleep 100")
    dm.set_restart_interval("test1", 86400)
    pid = dm.start_process("test1")
    assert dm.get_restart_interval("test1") == 86400
    dm.stop_process("test1")
    dm.wait_for_process_death(pid, timeout_seconds=5)
    # interval should still be set after stop + re-read
    assert dm.get_restart_interval("test1") == 86400


# ##################################################################
# test load state file resilient to empty file
# ensures the daemon doesn't crash when state.json is empty
def test_load_state_file_empty(temp_dir):
    state_path = dm.get_state_path()
    state_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.write_text("")
    state = dm._load_state_file()
    assert state == {"processes": {}}


# ##################################################################
# test load state file resilient to corrupt json
# ensures the daemon doesn't crash when state.json contains garbage
def test_load_state_file_corrupt(temp_dir):
    state_path = dm.get_state_path()
    state_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.write_text("{corrupt json!!!")
    state = dm._load_state_file()
    assert state == {"processes": {}}


# ##################################################################
# test load state file restores from backup on corruption
# ensures the backup is used when state.json is corrupt
def test_load_state_file_restores_backup(temp_dir):
    dm.add_process("myservice", "sleep 100")
    # corrupt the main file but leave backup intact
    state_path = dm.get_state_path()
    backup_path = state_path.with_suffix(".json.bak")
    assert backup_path.exists(), "backup should exist after save"
    state_path.write_text("")
    state = dm._load_state_file()
    assert "myservice" in state.get("processes", {})


# ##################################################################
# test save state file is atomic
# ensures a backup file is created alongside the state file
def test_save_state_file_creates_backup(temp_dir):
    dm.add_process("myservice", "sleep 100")
    state_path = dm.get_state_path()
    backup_path = state_path.with_suffix(".json.bak")
    assert state_path.exists()
    assert backup_path.exists()
    import json
    backup_data = json.loads(backup_path.read_text())
    assert "myservice" in backup_data.get("processes", {})
