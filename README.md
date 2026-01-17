![](banner.jpg)

# auto

A lightweight daemon process manager for macOS that keeps your services running reliably.

## Purpose

Auto manages background processes on your Mac. It starts services, keeps them running automatically, and restarts them if they crash. You register a process once, and auto ensures it stays alive—including after system restarts.

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
| `add <name> <command> [--port PORT]` | Add a new process and start it |
| `update <name> [--port PORT] [--workdir DIR]` | Update process settings |
| `remove <name>` | Stop and remove a process |
| `show <name>` | Display the command for a process |
| `log <name> [--tail] [--file]` | View process logs |
| `start-all` | Start all configured processes |
| `watch` | Monitor and restart crashed processes |
| `install` | Install the daemon and wrapper script |

## Uninstalling

```bash
# Stop all managed processes
for name in $(auto ps | tail -n +2 | awk '{print $1}'); do auto stop "$name"; done

# Unload the LaunchAgent
launchctl unload ~/Library/LaunchAgents/com.darrenoakey.auto.plist

# Remove files
rm ~/Library/LaunchAgents/com.darrenoakey.auto.plist
rm ~/bin/auto
rm -rf /path/to/auto
```