# Recording asciinema Demos with an Agent

How to create polished asciinema screencasts of exe.dev features using a coding agent.

## Prerequisites

```bash
sudo apt-get install -y asciinema python3-pexpect
asciinema auth   # user must do this — links to their asciinema.org account
```

## Running exed Locally

You need a local exed+sshpiperd stack. Build exelet first to avoid OOM:

```bash
make exelet
go build -o /tmp/exed-local ./cmd/exed/
```

Start exed with a **named DB path** (not `tmp`) so you can pre-populate it:

```bash
tmux new-session -d -s exed
tmux send-keys -t exed 'ANTHROPIC_API_KEY=... /tmp/exed-local -stage=local -start-exelet -db /tmp/demo-exed.db' Enter
```

Build and start sshpiperd (needs `GOTOOLCHAIN=go1.26.1` for deps/sshpiper):

```bash
GOTOOLCHAIN=go1.26.1 go build -o /tmp/sshpiperd ./deps/sshpiper/cmd/sshpiperd/
tmux new-session -d -s piper
tmux send-keys -t piper '/tmp/sshpiperd -p 2222 --server-key-generate-mode always --server-key /tmp/sshpiper-host-key --drop-hostkeys-message grpc --endpoint=localhost:2224 --insecure' Enter
```

Ports: sshpiperd listens on **2222**, exed SSH on **2223**, exed piper plugin on **2224**, exed HTTP on **8080**.

## Creating a Test User (Skip Registration)

Registration via the interactive SSH flow requires email verification which is painful to automate outside of e1e tests. Instead, insert a user directly into the DB:

```bash
# Generate an SSH key for the demo
ssh-keygen -t ed25519 -f /tmp/test-prompt-key -N '' -C 'demo'

# Compute the fingerprint the way exed does (hex-encoded SHA256 of raw key bytes)
FINGERPRINT=$(python3 -c "
import hashlib, base64
with open('/tmp/test-prompt-key.pub') as f:
    parts = f.read().strip().split()
    print(hashlib.sha256(base64.b64decode(parts[1])).hexdigest())
")

# The public_key column must match ssh.MarshalAuthorizedKey output:
# "ssh-ed25519 AAAA...\n" — type + base64 + newline, NO comment
PUBKEY_NO_COMMENT=$(awk '{print $1, $2}' /tmp/test-prompt-key.pub)

USER_ID="usr_demo_$(openssl rand -hex 8)"
ACCOUNT_ID="acct_demo_$(openssl rand -hex 8)"

sqlite3 /tmp/demo-exed.db <<SQL
INSERT INTO users (user_id, email, canonical_email, billing_exemption)
  VALUES ('$USER_ID', 'demo@exe.dev', 'demo@exe.dev', 'free');
INSERT INTO accounts (id, created_by)
  VALUES ('$ACCOUNT_ID', '$USER_ID');
INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment)
  VALUES ('$USER_ID', '${PUBKEY_NO_COMMENT}
', '$FINGERPRINT', 'demo-key');
SQL
```

**Critical details:**
- `public_key` must be `"type base64key\n"` (no comment, trailing newline) — this is what Go's `ssh.MarshalAuthorizedKey()` produces. If you include the comment from the `.pub` file, the key won't be found.
- `canonical_email` must be set (not empty) — `GetUserByEmail` queries on `canonical_email`, not `email`.
- `fingerprint` is `hex(sha256(raw_key_bytes))` — NOT the `SHA256:base64` format from `ssh-keygen -l`.
- The user needs a row in `accounts` or some features won't work.
- Set `billing_exemption='free'` to bypass billing checks.

Restart exed after DB changes (it doesn't hot-reload the SQLite DB for auth).

## Verify SSH Works

```bash
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -i /tmp/test-prompt-key -p 2222 localhost ls
```

Should show VMs (or "No VMs found"). If you see "Please complete registration", the user/key setup is wrong — check the three critical details above.

## Writing the Demo Driver Script

Use a Python pexpect script to automate the interaction with realistic typing. Key patterns:

```python
import pexpect, sys, time, random

def type_text(child, text, base_delay=0.04):
    """Simulate human typing."""
    time.sleep(0.3)
    for char in text:
        child.send(char)
        time.sleep(max(0.02, base_delay + random.uniform(-0.02, 0.04)))

child = pexpect.spawn(
    'ssh -tt -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null '
    '-o LogLevel=ERROR -i /tmp/test-prompt-key -p 2222 localhost',
    encoding='utf-8', timeout=60
)
child.logfile_read = sys.stdout       # Mirror to terminal (captured by asciinema)
child.setwinsize(35, 100)             # Match asciinema --rows/--cols

child.expect(r'▶', timeout=20)        # Wait for lobby prompt
# ... drive the demo ...
```

Expect patterns for the SSH lobby and prompt loop:
- Lobby prompt: `r'▶'`
- Prompt loop thinking: `r'thinking'`
- Follow-up prompt: `r'\n> '`
- Tool approval: `r'\[y/N\]'`
- Tool call indicator: `r'⚡'`

Send `stderr` debug messages to `/dev/null` when recording so they don't appear.

## Recording

```bash
TERM=xterm-256color asciinema rec /tmp/demo.cast \
    --title "My Demo Title" \
    --idle-time-limit 3 \
    --cols 100 \
    --rows 35 \
    --overwrite \
    -c "TERM=xterm-256color python3 /tmp/demo_driver.py 2>/dev/null" \
    -y
```

- `--idle-time-limit 3` caps pauses at 3 seconds for snappy playback
- `--cols 100 --rows 35` gives a good aspect ratio
- `-c` runs the driver script as the recorded command
- `2>/dev/null` suppresses driver debug output from the recording
- Set `TERM=xterm-256color` both outside and inside so colors render properly
- `-y` skips the upload confirmation prompt

## Fixing and Uploading

The recording may have `"TERM": null` in the header if `$TERM` wasn't set in the outer shell. Fix it before uploading:

```python
import json
with open('/tmp/demo.cast') as f:
    lines = f.readlines()
header = json.loads(lines[0])
header['env']['TERM'] = 'xterm-256color'
lines[0] = json.dumps(header) + '\n'
with open('/tmp/demo.cast', 'w') as f:
    f.writelines(lines)
```

Upload:

```bash
asciinema upload /tmp/demo.cast
```

## Iterating

Before recording, always do a dry run:

```bash
python3 /tmp/demo_driver.py   # watch it live, fix timing issues
```

Clean up state between takes (e.g., delete VMs created in the previous run):

```bash
ssh ... localhost rm my-demo-vm
```

Then restart exed if needed and record again.

## Troubleshooting

- **"Please complete registration"** — the user isn't found. Check: (1) `public_key` has no comment and has trailing `\n`, (2) `canonical_email` is populated, (3) `fingerprint` is hex SHA256 not base64, (4) exed was restarted after DB edits.
- **asciinema upload fails with "must include only string values"** — fix the `env` field in the `.cast` header (see above).
- **Blank/missing colors** — set `TERM=xterm-256color` in both the asciinema `rec` invocation and the `-c` command.
- **sshpiperd won't build** — needs `GOTOOLCHAIN=go1.26.1` because `deps/sshpiper/go.mod` requires go 1.25+.
