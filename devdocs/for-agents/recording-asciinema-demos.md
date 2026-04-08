# Recording asciinema Demos

How to create polished asciinema screencasts of exe.dev features using a coding agent.

## Prerequisites

```bash
sudo apt-get install -y asciinema python3-pexpect
asciinema auth   # user must do this -- links to their asciinema.org account
```

## Running exed Locally

Build exelet first to avoid OOM on small VMs:

```bash
make exelet
go build -o /tmp/exed-local ./cmd/exed/
```

Start exed with a **named DB path** (not `tmp`) so you can pre-populate it:

```bash
tmux new-session -d -s exed
tmux send-keys -t exed 'ANTHROPIC_API_KEY=... /tmp/exed-local -stage=local -start-exelet -db /tmp/demo-exed.db' Enter
```

Build and start sshpiperd (needs `GOTOOLCHAIN=go1.26.2` for `deps/sshpiper`):

```bash
GOTOOLCHAIN=go1.26.2 go build -o /tmp/sshpiperd ./deps/sshpiper/cmd/sshpiperd/
tmux new-session -d -s piper
tmux send-keys -t piper '/tmp/sshpiperd -p 2222 --server-key-generate-mode always --server-key /tmp/sshpiper-host-key --drop-hostkeys-message grpc --endpoint=localhost:2224 --insecure' Enter
```

Ports: sshpiperd **2222**, exed SSH **2223**, exed piper plugin **2224**, exed HTTP **8080**.

## Creating a Test User (Skip Registration)

Registration requires email verification which is hard to automate. Insert directly:

```bash
ssh-keygen -t ed25519 -f /tmp/test-prompt-key -N '' -C 'demo'

# Fingerprint: hex SHA256 of raw key bytes (NOT the SHA256:base64 format from ssh-keygen -l)
FINGERPRINT=$(python3 -c "
import hashlib, base64
with open('/tmp/test-prompt-key.pub') as f:
    parts = f.read().strip().split()
    print(hashlib.sha256(base64.b64decode(parts[1])).hexdigest())
")

# public_key must be "type base64key\n" -- no comment, trailing newline
PUBKEY_NO_COMMENT=$(awk '{print $1, $2}' /tmp/test-prompt-key.pub)

USER_ID="usr_demo_$(openssl rand -hex 8)"
ACCOUNT_ID="acct_demo_$(openssl rand -hex 8)"

sqlite3 /tmp/demo-exed.db <<SQL
INSERT INTO users (user_id, email, canonical_email)
  VALUES ('$USER_ID', 'demo@exe.dev', 'demo@exe.dev');
INSERT INTO accounts (id, created_by)
  VALUES ('$ACCOUNT_ID', '$USER_ID');
INSERT INTO ssh_keys (user_id, public_key, fingerprint, comment)
  VALUES ('$USER_ID', '${PUBKEY_NO_COMMENT}
', '$FINGERPRINT', 'demo-key');
SQL
```

**Critical details:**
- `public_key` must be `"type base64key\n"` (no comment, trailing newline) -- this is what Go's `ssh.MarshalAuthorizedKey()` produces.
- `canonical_email` must be set (not empty) -- `GetUserByEmail` queries on `canonical_email`.
- `fingerprint` is `hex(sha256(raw_key_bytes))` -- NOT `SHA256:base64` from `ssh-keygen -l`.
- The user needs a row in `accounts` or some features won't work.
- Billing is automatically skipped in local stage when no Stripe key is set (`SkipBilling` in `stage.go`).

Restart exed after DB changes (it doesn't hot-reload SQLite auth).

## Verify SSH Works

```bash
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -i /tmp/test-prompt-key -p 2222 localhost ls
```

Should show VMs (or "No VMs found"). If you see "Please complete registration", check the critical details above.

## Writing the Demo Driver Script

Use a Python pexpect script with realistic typing:

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

Expect patterns:
- Lobby prompt: `r'▶'`
- Thinking: `r'thinking'`
- Follow-up prompt: `r'\n> '`
- Tool approval: `r'\[y/N\]'`
- Tool call: `r'⚡'`

Send stderr to `/dev/null` when recording so debug messages don't appear.

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

- `--idle-time-limit 3` caps pauses at 3s
- `--cols 100 --rows 35` gives a good aspect ratio
- `-c` runs the driver script as the recorded command
- `2>/dev/null` suppresses driver debug output
- Set `TERM=xterm-256color` both outside and inside for correct colors
- `-y` skips upload confirmation

## Fixing and Uploading

The recording may have `"TERM": null` in the header. Fix before uploading:

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

```bash
asciinema upload /tmp/demo.cast
```

## Iterating

Dry run before recording:

```bash
python3 /tmp/demo_driver.py
```

Clean up state between takes (e.g., delete VMs from previous run), restart exed if needed, then record again.

## Troubleshooting

- **"Please complete registration"** -- user not found. Check: (1) `public_key` has no comment and has trailing `\n`, (2) `canonical_email` is populated, (3) `fingerprint` is hex SHA256 not base64, (4) exed was restarted after DB edits.
- **asciinema upload fails with "must include only string values"** -- fix the `env` field in the `.cast` header (see above).
- **Blank/missing colors** -- set `TERM=xterm-256color` in both the asciinema `rec` invocation and the `-c` command.
- **sshpiperd won't build** -- needs `GOTOOLCHAIN=go1.26.2` because `deps/sshpiper/go.mod` requires go 1.25+.
