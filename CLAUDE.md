# Auto Project

`auto` is a macOS daemon process manager, written in **Go** and shipped as a
**code-signed `Auto.app`** so it can hold a stable code identity. It keeps a set
of long-running services alive (restart on crash, restart on login, periodic
restarts) using a single `local/state.json` file for both definitions and runtime
state.

## Build / Run

The `run` script (bash facade) is the only entry point for builds:

- `./run build` — compile `cmd/*` and assemble + **code-sign** `output/Auto.app`.
- `./run check` — full quality gate: `gofmt`, `go vet`, `golangci-lint`, all tests.
- `./run install` (= `rebuild`) — build, sign, and (re)load the LaunchAgent.
- `./run logs` — tail the live daemon log.
- Any other verb is passed straight to the signed binary (`./run ps`, etc.).

Runtime commands (`ps start stop restart add update remove show status log
start-all stop-all restart-all watch shutdown install`) are handled by the Go
binary. `~/bin/auto` execs the signed app binary directly.

## Why signed (the whole point of the Go rewrite)

Services under the old Python `auto` did not get macOS **Local Network**
authorization: a bash/python launcher became the TCC "responsible process", so the
grant never stuck and the app never appeared in System Settings.

The fix is structural:
- `Auto.app` is a real bundle with `CFBundleIdentifier` + `NSLocalNetworkUsage-
  Description`, signed with a **stable** Apple Development identity (TCC keys on
  Team ID + bundle id, constant across rebuilds — see `~/src/sparkview`).
- launchd execs the signed binary **directly** (`ProgramArguments` =
  `Auto.app/Contents/MacOS/auto watch`), so `auto` is its OWN responsible process.
- Every service `auto` spawns inherits `auto` as the responsible process, so **one**
  Local Network grant (made once in System Settings → Privacy & Security → Local
  Network) covers all managed services, and it persists across rebuilds.

A child must be spawned **by the launchd daemon** to inherit auto's identity.
A service spawned by a terminal `auto start` is attributed to the terminal instead.
To re-parent everything under the signed daemon, `auto stop-all` kills all managed
processes (without marking them stopped) and the watch loop respawns them.

Note: an app that needs its own dock presence / window (e.g. sparkview) runs as its
OWN signed LaunchAgent, not under auto.

## Architecture (src/)

- `cmd/auto` — CLI dispatch, watch loop (SIGTERM → clean teardown), banner, App Nap
  opt-out (CGo, darwin), and the `bundle/Info.plist` for the app bundle.
- `pkg/manager` — all supervision logic on a `Manager` rooted at the project tree.
- `pkg/install` — writes `~/bin/auto`, generates + loads the LaunchAgent plist.

### State file (`local/state.json`)
One JSON object `{"processes": {name: {...}}}`. Each entry carries both definition
(`command`, `port`, `workdir`) and runtime (`pid`, `start_time`, `explicitly_stopped`,
`restart_attempt`, `last_restart_time`, `log_path`, `restart_interval_seconds`,
`last_periodic_restart`). Field names/nullability match the original Python file so
old state loads unchanged. Entries **without a command** are runtime-only stubs and
are ignored everywhere (`definedNames`), matching the old `load_config`.

### State writes are LOCKED (critical)
Every load-modify-save goes through `Manager.withState`, which holds an exclusive
`flock` on `local/state.lock` for the whole transaction. This serializes the watch
daemon and any concurrent CLI invocation. **Do not** add a state mutation that
loads and saves without `withState` / `mutateProcess` — concurrent read-modify-write
without the lock previously wiped the entire state file. `mutateProcess` also never
creates an entry for an unknown name (no stub resurrection of removed services).
`saveStateFile` writes atomically (unique temp + rename) and refreshes `.bak`.

### PID identity
A process is "ours" only if its pid is alive AND its `ps -o lstart=` start time
matches the recorded one — defeats PID reuse after reboot. The lstart parser accepts
both US (`Mon Jan 26 …`) and en_AU (`Mon 26 Jan …`) locale forms; the LaunchAgent
sets `LANG=en_AU.UTF-8` for consistency.

### Restart backoff
Exponential (1s, 2s, 4s …) capped at `MaxRestartBackoff` (5 min), with a stable
per-name jitter so simultaneous backoffs don't fire in lockstep. Reset after
`SuccessfulStartThreshold` (60s) of stable uptime. The exponent is bounded
(`maxBackoffExp`) so `1<<n * time.Second` can never overflow and defeat the cap.
The watch loop caps fresh starts per tick (`MaxRestartsPerWatchTick`) so a
post-reboot mass start doesn't fire every fork at once.

### Spawning
`/bin/sh -c "exec <command>"` with `Setsid` (new session/process group), cwd =
workdir, stdout+stderr → a timestamped log under `output/logs/<name>/YYYY/MM/`.
Transient host fork/exec failures (EDEADLK/EAGAIN/ENOMEM) and async execve deaths
(transient markers in the log) are retried. Surviving children get a `Wait4` reaper
goroutine so the long-lived daemon never accumulates zombies.

### Shutdown vs reload
- System shutdown sends SIGTERM → the watch loop tears down all managed processes
  cleanly (NOT marked explicitly stopped → they restart next boot).
- `launchctl bootout` also SIGTERMs the daemon → same clean teardown; install uses
  bootout+bootstrap. (`kickstart -k` SIGKILLs the whole job tree, killing the
  managed services anyway, so it offers nothing better.)
- Either way the fresh daemon restarts everything; managed services briefly drop
  during a daemon reinstall. This is inherent to the launchd-job model.

## Gotchas

- **Commands are relative to `workdir`** — use `./run serve`, not `run serve`
  (a bare `run` is not on PATH and fails with `exec: run: not found`).
- `auto add <name> <cmd>` sets `workdir` to the **current directory**; cd into the
  project first, or fix it later with `auto update <name> --workdir <dir>`.
- Reading Time Machine backups needs Full Disk Access (TCC); `sudo tmutil`/`sudo
  cat` are blocked without it. `sudo` CAN attach the sparsebundle and list
  snapshots, but not read the backed-up files.

## Keep the skill in sync
When changing commands/behaviour, also update `~/.claude/skills/auto/SKILL.md`.
