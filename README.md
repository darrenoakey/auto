![](banner.jpg)

# Auto - Daemon Process Manager

A simple daemon process manager for macOS that makes it easy to add and manage long-running background processes that start automatically at login.

## Purpose

Auto helps you manage daemon processes that need to run continuously in the background. It handles process lifecycle (start, stop, restart), automatic startup at login, and optional automatic restart of crashed processes. Each process runs independently with its output logged to separate files.

## Installation

1. Clone or copy the project to `~/local/auto/`
2. Create a symbolic link to make the command available globally:
   ```bash
   ln -s ~/local/auto/run ~/bin/auto
   chmod +x ~/local/auto/run
   ```
3. Set up the macOS LaunchAgent for automatic startup at login:
   ```bash
   cp ~/local/auto/com.darrenoakey.auto.plist ~/Library/LaunchAgents/
   launchctl load ~/Library/LaunchAgents/com.darrenoakey.auto.plist
   ```

## Usage

### List all processes

View all configured processes and their current status (PID or "dead"):

```bash
auto ps
```

### Add a new process

Add a process to the configuration and start it immediately:

```bash
auto add <name> <command...>
```

Example:
```bash
auto add my-server python3 /path/to/server.py --port 8080
auto add backup-sync rsync -av /source/ /backup/
```

### Start a process

Start a configured process that is currently stopped:

```bash
auto start <name>
```

### Stop a process

Stop a running process by sending SIGTERM:

```bash
auto stop <name>
```

### Restart a process

Stop and then start a process:

```bash
auto restart <name>
```

### Show process command

Display the full command line for a configured process:

```bash
auto show <name>
```

### Remove a process

Stop a process if running and remove it from configuration:

```bash
auto remove <name>
```

### Start all processes

Start all configured processes (automatically run at login by LaunchAgent):

```bash
auto start-all
```

### Watch and auto-restart

Monitor all processes and automatically restart any that crash (with exponential backoff):

```bash
auto watch
```

## Examples

### Running a web server

```bash
# Add and start a Flask development server
auto add flask-dev python3 /home/user/myapp/server.py

# Check if it's running
auto ps

# View the logs
tail -f ~/local/auto/output/logs/flask-dev.stdout.log

# Restart after making changes
auto restart flask-dev
```

### Managing multiple services

```bash
# Add several services
auto add api-server node /opt/api/server.js
auto add worker python3 /opt/worker/main.py
auto add monitor ./monitoring-script.sh

# Check status of all
auto ps

# Stop one temporarily
auto stop worker

# Remove one permanently
auto remove monitor
```

### Automatic restart on crash

```bash
# Start watching mode in a dedicated terminal or screen session
auto watch

# Now if any process crashes, it will be automatically restarted
# with exponential backoff (1s, 2s, 4s, 8s, up to 60s between attempts)
```

## Process Logs

All process output is captured to files in `output/logs/`:

- `<name>.stdout.log` - Standard output
- `<name>.stderr.log` - Standard error
- `auto.stdout.log` - LaunchAgent startup log

## Managing the LaunchAgent

The LaunchAgent automatically starts all configured processes at login.

### Disable automatic startup
```bash
launchctl unload ~/Library/LaunchAgents/com.darrenoakey.auto.plist
```

### Enable automatic startup
```bash
launchctl load ~/Library/LaunchAgents/com.darrenoakey.auto.plist
```

### Check LaunchAgent status
```bash
launchctl list | grep com.darrenoakey.auto
```

## Configuration Files

- `config.json` - Process definitions (name to command mapping)
- `state.json` - Runtime state (PIDs and restart tracking)
- `output/logs/` - Process output logs (automatically created, gitignored)