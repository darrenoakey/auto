# Auto Project

## Important: Keep Skill Definition in Sync

When making changes to this project (commands, features, usage, etc.), you MUST also update the corresponding skill definition at:

`~/.claude/skills/auto`

This ensures Claude Code's skill system stays in sync with the actual implementation.

## Key Architecture

### PID Identity Verification

Processes are tracked by both `pid` AND `start_time` in state.json. This handles PID reuse after reboot:
- After reboot, old PIDs get reused by different system processes
- `is_our_process(pid, start_time)` verifies both match before considering a process "alive"
- If no `start_time` stored (old entries), treats as stale to force restart with proper tracking

### LaunchAgent Environment

The plist at `~/Library/LaunchAgents/com.darrenoakey.auto.plist` must include `LANG` in EnvironmentVariables:
- Without LANG, `ps -o lstart=` returns US format: `Mon Jan 26`
- With `en_AU` locale, same command returns: `Mon 26 Jan`
- `_parse_lstart_time()` handles both formats, so mixed format entries in state.json work correctly
- LANG should still be set consistently to avoid confusion when reading state.json manually

### SIGTERM Graceful Shutdown

The watch loop (`command_watch`) registers a SIGTERM handler that raises `ShutdownRequested`:
- During macOS system shutdown, SIGTERM is sent to all user processes
- Without the handler, watch would detect dying managed processes and restart them, fighting the shutdown
- The handler interrupts both `time.sleep()` and `watch_and_restart_processes()` mid-execution
- On SIGTERM: stops the loop, calls `shutdown_all_processes()` (which does NOT mark processes as `explicitly_stopped`), exits cleanly
- After reboot, processes will be auto-restarted by the watch loop since they aren't marked as explicitly stopped

### Process Status Display

`auto ps` shows three states for the PID column:
- `<pid>` — process is running
- `stopped` — user explicitly stopped it (`auto stop`), watch won't restart it
- `dead` — process died unexpectedly, watch will restart it (with backoff)

## Gotchas

### "Process shows running but isn't responding"

1. Check if PID actually belongs to expected process: `ps -p <pid> -o comm,args`
2. After reboot, PIDs get reused - state.json may have stale entries
3. Use `lsof -i :<port>` to find what's actually listening

### Port conflicts during restart

When auto watch restarts a process that fails immediately (e.g., port conflict), the state is updated with the failed process's PID. If the original process is still running, this creates a mismatch. Solution: kill the old process and reset state, or let auto watch handle it with backoff.

## Process Management Patterns

### Process Group Killing

Processes are started with `start_new_session=True` which creates a new process group:
- This is critical for services like uvicorn that spawn worker/reloader subprocesses
- When stopping, MUST use `os.killpg(pgid, signal)` to kill the entire group
- Using `os.kill(pid, signal)` only kills the parent, leaving orphaned children
- Get pgid with `os.getpgid(pid)` before sending signals

### SIGTERM→SIGKILL Escalation

`stop_process()` uses a two-stage termination pattern:
1. Send SIGTERM to process group, wait 5 seconds (`SIGTERM_TIMEOUT`)
2. If still alive, send SIGKILL to process group, wait 5 seconds (`SIGKILL_TIMEOUT`)
3. If still alive after SIGKILL, raise RuntimeError

This handles stubborn processes that trap SIGTERM (e.g., shell scripts with `trap '' TERM`).

### Port Pre-flight Checks

`start_process()` checks port availability BEFORE spawning subprocess:
- Uses `is_port_free(port)` to attempt binding the port
- If occupied, raises RuntimeError with clear message including `lsof -i :<port>` hint
- Prevents crash loops when ports are stuck bound by zombie processes
- Automatically inherited by watch loop restarts (no special handling needed)

### Exponential Backoff with Stability Reset

Restart backoff doubles with each failure (1s, 2s, 4s... up to 2 hours):
- `restart_attempt` counter tracks consecutive failures
- `last_restart_time` records when restart was attempted
- `check_and_reset_backoff()` monitors process uptime
- After 60 seconds of stable operation (`SUCCESSFUL_START_THRESHOLD`), backoff resets to 0
- Prevents indefinite backoff accumulation for normally stable processes
- Called every watch cycle for running processes
