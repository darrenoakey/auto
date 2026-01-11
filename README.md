![](banner.jpg)

# auto

A lightweight daemon process manager for macOS that keeps your services running reliably. Built with pure Python and zero external dependencies.

## Features

- **Process Lifecycle Management** - Start, stop, restart, and monitor background processes
- **Automatic Restart with Exponential Backoff** - Crashed processes are automatically restarted with intelligent backoff (1s, 2s, 4s... up to 2 hours)
- **macOS LaunchAgent Integration** - Automatically starts on login and keeps the watcher daemon alive
- **Organized Logging** - Logs stored in `output/logs/{process}/{year}/{month}/` with timestamps
- **Explicit Stop Protection** - Processes you manually stop won't be auto-restarted
- **Working Directory Support** - Run processes from specific directories
- **Port Tracking** - Associate ports with processes for reference
- **Pure Python** - No external dependencies, uses only the Python standard library

## Installation

### Prerequisites

- macOS (uses LaunchAgent for auto-start)
- Python 3.10+
- `~/bin` in your PATH (optional, for global `auto` command)

### Quick Install

```bash
# Clone the repository
git clone https://github.com/darrenoakey/auto.git
cd auto

# Run the installer
./run install
```

The installer will:
1. Create a wrapper script at `~/bin/auto` for global access
2. Install and load a LaunchAgent for automatic startup
3. Verify the installation was successful

If `~/bin` is not in your PATH, add this to your shell profile (`.zshrc`, `.bashrc`, etc.):
```bash
export PATH="$HOME/bin:$PATH"
```

## Usage

### Basic Commands

```bash
# List all processes with their status
auto ps

# Add a new process and start it
auto add myapp "python3 /path/to/app.py"

# Start a stopped process
auto start myapp

# Stop a running process
auto stop myapp

# Restart a process
auto restart myapp

# Show the command for a process
auto show myapp

# Remove a process (stops it if running)
auto remove myapp
```

### Advanced Usage

```bash
# Start all configured processes
auto start-all

# Watch and auto-restart crashed processes (runs continuously)
auto watch
```

## Commands Reference

| Command | Description |
|---------|-------------|
| `ps` | List all configured processes with their PIDs (or "dead" if not running) |
| `start <name>` | Start a configured process |
| `stop <name>` | Stop a running process (marks as explicitly stopped) |
| `restart <name>` | Stop and start a process |
| `add <name> <command>` | Add a new process to the configuration and start it |
| `remove <name>` | Stop and remove a process from the configuration |
| `show <name>` | Display the command line for a process |
| `start-all` | Start all configured processes that aren't running |
| `watch` | Continuously monitor and restart crashed processes |
| `install` | Install the daemon and wrapper script |

## How It Works

### Architecture

```
auto/
├── run                 # Main entry point (Python script)
├── src/
│   ├── daemon_manager.py    # Core process management logic
│   └── installer.py         # Installation utilities
├── local/
│   └── state.json      # Process definitions and runtime state
└── output/
    └── logs/           # Process log files
        ├── auto/       # Daemon manager logs
        └── {process}/  # Per-process logs organized by date
            └── {year}/
                └── {month}/
                    └── {process}_{timestamp}.log
```

### State Management

All process configuration and runtime state is stored in `local/state.json`:

```json
{
  "processes": {
    "myapp": {
      "command": "python3 /path/to/app.py",
      "workdir": "/path/to",
      "port": 8080,
      "pid": 12345,
      "explicitly_stopped": false,
      "restart_attempt": 0,
      "last_restart_time": null,
      "log_path": "/path/to/auto/output/logs/myapp/2024/01/myapp_240115_143022.log"
    }
  }
}
```

### Automatic Restart Behavior

When a process dies unexpectedly:

1. The `watch` command detects the dead process
2. If not explicitly stopped by the user, it schedules a restart
3. Restart attempts use exponential backoff:
   - Attempt 0: 1 second
   - Attempt 1: 2 seconds
   - Attempt 2: 4 seconds
   - Attempt 3: 8 seconds
   - ... up to a maximum of 2 hours
4. When manually started, the backoff counter resets

### LaunchAgent

The installer creates a LaunchAgent at `~/Library/LaunchAgents/com.darrenoakey.auto.plist` that:

- Runs `./run watch` to monitor processes
- Starts automatically on login (`RunAtLoad`)
- Restarts if it crashes (`KeepAlive`)
- Captures stdout/stderr to `output/logs/auto/auto.log`

## Examples

### Running a Web Server

```bash
# Add a Flask app
auto add flask-api "python3 -m flask run --host=0.0.0.0 --port=5000"

# Add a FastAPI app with uvicorn
auto add fastapi-app "uvicorn main:app --host 0.0.0.0 --port 8000"
```

### Running Background Workers

```bash
# Add a Celery worker
auto add celery-worker "celery -A myapp worker --loglevel=info"

# Add a custom background script
auto add data-sync "/path/to/sync_data.sh"
```

### Managing Multiple Services

```bash
# Add several services
auto add api-server "python3 api/server.py"
auto add web-ui "npm start --prefix /path/to/frontend"
auto add redis "redis-server /etc/redis.conf"

# Check their status
auto ps

# Output:
# api-server: 12345
# redis: 12347
# web-ui: 12346
```

## Logs

Process logs are organized hierarchically:

```
output/logs/
├── auto/
│   └── auto.log                           # Daemon manager logs
├── myapp/
│   └── 2024/
│       └── 01/
│           ├── myapp_240115_143022.log    # Log from Jan 15
│           └── myapp_240116_091500.log    # Log from Jan 16
└── other-process/
    └── ...
```

Each process restart creates a new timestamped log file, making it easy to track historical behavior.

## Troubleshooting

### Process won't restart automatically

Check if it was explicitly stopped:
```bash
# If you used 'auto stop', the process is marked as explicitly stopped
# To allow auto-restart, start it manually first:
auto start myapp
```

### LaunchAgent not running

```bash
# Check if loaded
launchctl list | grep com.darrenoakey.auto

# Reload if needed
launchctl unload ~/Library/LaunchAgents/com.darrenoakey.auto.plist
launchctl load ~/Library/LaunchAgents/com.darrenoakey.auto.plist
```

### View daemon logs

```bash
# See what the watch daemon is doing
tail -f output/logs/auto/auto.log
```

### View process logs

```bash
# Find the latest log for a process
ls -la output/logs/myapp/$(date +%Y)/$(date +%m)/

# Tail the most recent log
tail -f output/logs/myapp/$(date +%Y)/$(date +%m)/myapp_*.log
```

## Development

### Running Tests

```bash
# Run all tests
pytest src/

# Run with verbose output
pytest -v src/
```

### Project Structure

```
auto/
├── run                          # CLI entry point
├── src/
│   ├── daemon_manager.py        # Core daemon management
│   ├── daemon_manager_test.py   # Test suite
│   └── installer.py             # Installation logic
├── local/                       # Runtime data (gitignored)
│   └── state.json               # Process config and state
├── output/                      # Logs and outputs (gitignored)
│   └── logs/
├── com.darrenoakey.auto.plist   # LaunchAgent template
├── pytest.ini                   # Pytest configuration
└── requirements.txt             # No external dependencies
```

## Uninstalling

```bash
# Stop all managed processes
auto stop-all 2>/dev/null || true

# Unload the LaunchAgent
launchctl unload ~/Library/LaunchAgents/com.darrenoakey.auto.plist

# Remove the LaunchAgent
rm ~/Library/LaunchAgents/com.darrenoakey.auto.plist

# Remove the wrapper script
rm ~/bin/auto

# Remove the project directory
rm -rf /path/to/auto
```

## License

MIT License - see LICENSE file for details.
