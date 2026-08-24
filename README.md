![](banner.jpg)

# auto

A lightweight daemon process manager for macOS that keeps your services running reliably.

## Purpose

Auto manages background processes on your Mac. It starts services, keeps them running automatically, and restarts them if they crash. You register a process once, and auto ensures it stays alive—including after system restarts.

Auto is written in Go and ships as a code-signed `Auto.app`. Because the watch daemon runs as its own stably-signed responsible process, every service it spawns inherits auto's code identity—so a single macOS **Local Network** grant (System Settings → Privacy & Security → Local Network) covers all managed services and persists across rebuilds.

## Installation

```bash
# Clone the repository
git clone https://github.com/darrenoakey/auto.git
cd auto

# Run the installer
./run install
```

The installer creates a global `auto` command in `~/bin` and sets up a LaunchAgent so the watcher runs automatically on login.

If `~/bin` is not in your PATH, add this to your shell profile:

```bash
export PATH="$HOME/bin:$PATH"
```

## Usage

### List Processes

```bash
auto ps
```

Shows all configured processes with their PIDs and ports.

### Add a Process

```bash
auto add myapp "python3 /path/to/app.py"
```

Adds a new process and starts it immediately.

```bash
auto add myapi "python3 /path/to/api.py" --port 8080
```

Adds a process with port information for reference.

### Start, Stop, and Restart

```bash
auto start myapp
auto stop myapp
auto restart myapp
```

### View Process Details

```bash
auto show myapp
```

Displays the command configured for the process.

### View Logs

```bash
auto log myapp
```

Shows the latest log file contents.

```bash
auto log myapp --tail
```

Follows the log file in real-time.

```bash
auto log myapp --file
```

Prints just the log file path.

Process stdout/stderr go to a daily file
`output/logs/<name>/YYYY/MM/<name>_YYYY-MM-DD.log`. Spawns the same day append
to that file. The watch daemon zips previous-day (and older) plain logs to
sibling `*.log.zip` archives in the background and deletes the plain file;
today's live log is never archived.

### Update Process Settings

```bash
auto update myapp --port 3000
auto update myapp --workdir /path/to/dir
```

### Remove a Process

```bash
auto remove myapp
```

Stops the process and removes it from the configuration.

### Start All Processes

```bash
auto start-all
```

Starts all configured processes that aren't currently running.

### Watch Mode

```bash
auto watch
```

Continuously monitors processes and restarts any that crash. This runs automatically via LaunchAgent after installation.

## Windowed GUI apps showing wildly inflated / identical memory in Force Quit

**Symptom:** Two or more unrelated `auto`-managed GUI apps (e.g. a tiny ~100MB
dashboard and another tiny app) all show the exact same multi-GB figure in
**Force Quit Applications** (⌥⌘Esc) and in the "out of application memory"
critical-pressure dialog, even though each process's own `phys_footprint`
(`footprint <pid>`, `vmmap -summary`, Activity Monitor's own live reading) is
small and correct.

**Root cause:** every process `auto watch` forks/execs is a normal child of
the `auto` daemon, so it inherits the daemon's own XNU *resource coalition*
(kernel-level grouping used for jetsam/memory-pressure accounting — see
`powermetrics --show-process-coalition`). If `auto` also manages ANY
memory-heavy service (an LLM server, a big Electron/Chromium child, etc.),
that service ends up in the **same coalition** as every other `auto`-managed
app. macOS's Force Quit / "out of application memory" view attributes the
whole coalition's aggregate memory to every *visible-window* member of that
coalition — so a 100MB dashboard shows the multi-GB total of whatever heavy
sibling shares its coalition, identically to every other windowed sibling.
This is a real macOS accounting behavior, not a leak in the small apps, and
`vm_stat`/`footprint`/`vmmap` per-process readings never show it because
they report true per-process figures, not the coalition aggregate.

**Permanent fix — give the process its own coalition:** only launchd itself
(via `launchctl bootstrap` of a **separate LaunchAgent**, one per app) can
create a fresh coalition; a plain `auto`-managed fork/exec can't opt out.

`auto` has a **built-in `--isolate` mode** that does this for you — no
hand-rolled per-app bundle/plist/launchctl scripting needed:

```bash
auto stop myapp
auto update myapp --isolate on
auto start myapp
```

`--isolate on` registers a dedicated `com.darrenoakey.<name>` LaunchAgent
(plist under `~/Library/LaunchAgents`) that execs the process directly via a
tiny compiled shim (`~/bin/auto-shim`), so `start`/`stop`/`restart`/`ps` keep
the exact same interface — internally `StartProcess`/`StopProcess`/
`RestartProcess`/`processStatus` all delegate to `launchctl
bootstrap`/`kickstart`/`bootout`/`list` instead of forking directly, and
`auto watch`'s own tick skips isolate-mode processes entirely (launchd fully
owns their liveness/restart — auto never spawns or restarts them). `auto ps`
shows the mode in its `MODE` column (`auto` vs `isolate`). Revert with
`auto stop myapp && auto update myapp --isolate off` — the flag refuses to
flip while the process is alive under its current mode, so you can't
silently orphan a running instance. `auto remove` on an isolate-mode process
boots the launchd job out and deletes its plist, leaving no trace.

Verify with `powermetrics -i 1000 -n 1 --samplers tasks
--show-process-coalition` — the app must appear as the sole member of its
own `com.darrenoakey.<name>` coalition, not nested under
`com.darrenoakey.auto`.

This is exactly the pattern `activity` (`~/src/activity/run` → `cmd_bundle`)
and `agentd-gauge` (`~/src/agentd-gauge/run` → `cmd_bundle`) already used by
hand-rolling their own hand-written plist + `launchctl bootstrap` steps
before `--isolate` existed; both are compiled Go/Gio binaries, which is the
case this pattern reliably fixes.

**Confirmed limitation — Python / interpreter-embedding apps CANNOT
currently use `--isolate` on this machine (macOS 26 Tahoe beta), even
through the compiled shim:** a Python (including PyInstaller-bundled
Mach-O) process spawned by a **freshly bootstrapped** LaunchAgent hangs
indefinitely during interpreter initialization (confirmed stuck inside
`Py_InitializeFromConfig`, 0% CPU, ~16MB RSS, zero file I/O, for 25+
seconds) regardless of *how* it is invoked — directly, via `open -W -n
<bundle>`, or via `auto-shim`'s own `syscall.Exec("/bin/sh", "-c", "exec
...")` (tested 2026-08-25 on `calendar-display`/PySide6: `auto update
calendar-display --isolate on` reproduced the identical hang; reverted with
`--isolate off`). The shim does not help because `syscall.Exec` replaces
the process image in place — the pid launchd is tracking as its direct
child is still, ultimately, the same Python process either way; only the
intermediate argv/image differs, not the launchd-spawn characteristic that
actually triggers the hang. The exact same binary launched by an
already-running warm process (e.g. `auto` itself, or an interactive shell)
starts normally in seconds. Also reproduced earlier with `nativ`'s
`mlx-vlm-server` (PyInstaller). **Do not** retry any variation of routing a
Python/interpreter-based `auto`-managed app through a dedicated LaunchAgent
(direct, `open`, or shim) until this is confirmed fixed in a later macOS
release — leave them under plain `auto` (`--isolate off`, the default) and
accept they'll share `auto`'s coalition. If a Python app's inflated-memory
reading actually matters, the only reliable fix today is porting it to a
compiled language (as `activity`/`agentd-gauge` already are, in Go+Gio) so
it can use `--isolate`.

`auto log <name> --tail` **follows forever and blocks** — never run it from
an agent/script without a hard timeout; use plain `auto log <name>` for a
one-shot dump.

## Examples

### Running a Web Server

```bash
auto add flask-api "python3 -m flask run --host=0.0.0.0 --port=5000" --port 5000
auto add fastapi-app "uvicorn main:app --host 0.0.0.0 --port 8000" --port 8000
```

### Running Background Workers

```bash
auto add celery-worker "celery -A myapp worker --loglevel=info"
auto add data-sync "/path/to/sync_data.sh"
```

### Managing Multiple Services

```bash
auto add api-server "python3 api/server.py" --port 8000
auto add web-ui "npm start --prefix /path/to/frontend" --port 3000
auto add redis "redis-server /etc/redis.conf"

auto ps
# NAME        PID   PORT
# api-server  12345  8000
# redis       12347     -
# web-ui      12346  3000
```

## Commands Reference

| Command | Description |
|---------|-------------|
| `ps` | List all processes with their status |
| `start <name>` | Start a configured process |
| `stop <name>` | Stop a running process |
| `restart <name>` | Stop and start a process |
| `add <name> <command> [--port PORT] [--isolate on\|off]` | Add a new process and start it |
| `update <name> [--port PORT] [--workdir DIR] [--isolate on\|off]` | Update process settings |
| `remove <name>` | Stop and remove a process |
| `show <name>` | Display the command for a process |
| `log <name> [--tail] [--file]` | View process logs |
| `start-all` | Start all configured processes |
| `stop-all` | Stop all running processes (watch daemon respawns them) |
| `restart-all` | Restart all dead, non-stopped processes |
| `watch` | Monitor and restart crashed processes |
| `shutdown` | Stop everything and the daemon for a clean reboot |
| `install` | Build, sign, and load the daemon and wrapper script |

## Uninstalling

```bash
# Stop all managed processes
for name in $(auto ps | tail -n +2 | awk '{print $1}'); do auto stop "$name"; done

# Unload the LaunchAgent
launchctl bootout gui/$(id -u)/com.darrenoakey.auto

# Remove files
rm ~/Library/LaunchAgents/com.darrenoakey.auto.plist
rm ~/bin/auto
rm -rf /path/to/auto
```

## License

This project is licensed under [CC BY-NC 4.0](https://darren-static.waft.dev/license) - free to use and modify, but no commercial use without permission.
