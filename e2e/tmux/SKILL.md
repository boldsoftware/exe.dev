---
name: tmux
description: Use tmux for non-blocking program testing - run services, send input, capture output without blocking.
---

# Using tmux for Non-Blocking Program Testing

You will use **tmux** to run and test programs in a way that never blocks your own process.
Use a **single tmux session** with multiple **windows** - each window corresponds to a single command or program you want to run.

## Key Concepts

* A **session** is the main tmux container - create one for your entire test run
* A **window** is a named terminal within the session - use one per program/command
* You run one program per window
* You interact by sending keystrokes into windows and capturing their output
* All tmux commands return immediately

## Start the main session

First, create your main testing session:

```bash
tmux new-session -d -s testing
```

This creates a detached session called `testing` with a default window.

## Create a new window with a command

```bash
tmux new-window -t testing -n web 'python3 -m http.server 8000'
```

This creates a new window called `web` in the `testing` session, running a Python HTTP server.

## Create an empty window (for interactive commands)

```bash
tmux new-window -t testing -n build
```

This creates an empty window called `build` where you can send commands later.

## Send input to a specific window

```bash
tmux send-keys -t testing:build 'make build' C-m
```

This types into the `build` window of the `testing` session.

## Read output from a specific window

```bash
tmux capture-pane -p -S -50 -E -1 -t testing:web
```

This prints the last 50 lines from the `web` window.

## List windows in your session

```bash
tmux list-windows -t testing
```

## Check if a window exists

```bash
tmux list-windows -t testing | grep -q '^[0-9]*: web'
```

## Kill a specific window

```bash
tmux kill-window -t testing:web
```

## Kill the entire session when done

```bash
tmux kill-session -t testing
```

## Example workflow

```bash
# Start main session
tmux new-session -d -s testing

# Create windows for different services
tmux new-window -t testing -n exed
tmux new-window -t testing -n sshpiper
tmux new-window -t testing -n client

# Start services in their windows
tmux send-keys -t testing:exed 'cd /app && make dev' C-m
tmux send-keys -t testing:sshpiper './deps/sshpiper/cmd/sshpiperd/sshpiperd' C-m

# Use client window for interactive testing
tmux send-keys -t testing:client 'ssh -p 2222 localhost' C-m

# Check outputs
tmux capture-pane -p -t testing:exed
tmux capture-pane -p -t testing:client

# Clean up when done
tmux kill-session -t testing
```
