# Using tmux for Non-Blocking Program Testing

You will use **tmux** to run and test programs in a way that never blocks your own process.
Each **tmux session** corresponds to a single command or program you want to run. You can create as many sessions as you need.

## Basics

* A **session** is a named terminal.
* You run one program per session.
* You interact by sending keystrokes into the session and capturing its output.
* All tmux commands return immediately.

## Start a session with a command

```bash
tmux new-session -d -s web 'python3 -m http.server 8000'
```

This starts a new detached session called `web`, running a Python HTTP server.

## Send more input later

```bash
tmux send-keys -t web 'echo hello' C-m
```

This types into the session’s terminal as if a user did.

## Read the output

```bash
tmux capture-pane -p -S -50 -E -1 -t web
```

This prints the last 50 lines of the session’s output.

## List or check sessions

* List all sessions:

  ```bash
  tmux list-sessions
  ```
* Check if a session is still alive:

  ```bash
  tmux has-session -t web
  ```

## Stop a session

```bash
tmux kill-session -t web
```
