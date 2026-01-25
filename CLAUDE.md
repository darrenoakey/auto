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
- Mismatch causes valid processes to be detected as stale

## Gotchas

### "Process shows running but isn't responding"

1. Check if PID actually belongs to expected process: `ps -p <pid> -o comm,args`
2. After reboot, PIDs get reused - state.json may have stale entries
3. Use `lsof -i :<port>` to find what's actually listening

### Port conflicts during restart

When auto watch restarts a process that fails immediately (e.g., port conflict), the state is updated with the failed process's PID. If the original process is still running, this creates a mismatch. Solution: kill the old process and reset state, or let auto watch handle it with backoff.
