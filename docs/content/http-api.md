---
title: HTTPS API
description: Programmatic access via HTTPS
subheading: "2. Features"
preview: true
---

The HTTPS API enables programmatic HTTPS access both to exe.dev and to individual VMs.

## exe.dev

The exe.dev HTTPS API is nothing but the SSH API shoved into a POST body.

This might seem crazy, but it means you have only one API to learn, and you can develop and debug all your API calls interactively over SSH.

All requests use the same endpoint:

```bash
POST https://exe.dev/exec
```

The POST body is the ssh command to run, exactly as if it were typed into the REPL or exec'd via ssh. JSON output is always enabled for API responses (equivalent to `--json`). The returned body is the ssh output. That's it.

See the [CLI reference](/docs/section/8-cli-reference) docs section for the full list of available commands.

## Authentication

Authentication uses tokens that you sign locally with your SSH private key. (Surprise!)

Generating them is a bit harder than clicking a button on a website, but can be done entirely locally and programmatically.

### Quick start

#### Add a new SSH key to your exe.dev account

You don't _have_ to do this, but it's a good idea: you can revoke this API key by removing this ssh key from exe.dev, without disrupting your regular ssh access. The `-C` flag sets a name for this ssh key; feel free to change it.

```bash
ssh-keygen -t ed25519 -C api -f ~/.ssh/exe_dev_api
```

```bash
cat ~/.ssh/exe_dev_api.pub | ssh exe.dev ssh-key add
```

If you want finer-grained revocability of API keys, add more ssh keys.

#### Generate a token using this ssh key

Permissions are specified as JSON. Each field overrides a default; see [below](#granular-permissions) for all available fields. We'll use `{}` for now; this creates a token that never expires.

Define a helper to convert base64 to base64url ([RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648#section-5)).

```bash
b64url() { tr -d '\n=' | tr '+/' '-_'; }
```

Set the permissions and base64url-encode them.

```bash
export PERMISSIONS='{}'
```

```bash
export PAYLOAD=$(printf '%s' "$PERMISSIONS" | base64 | b64url)
```

Sign the permissions with your SSH key.

```bash
export SIG=$(printf '%s' "$PERMISSIONS" | ssh-keygen -Y sign -f ~/.ssh/exe_dev_api -n v0@exe.dev)
```

Strip the PEM armor and convert to base64url.

```bash
export SIGBLOB=$(echo "$SIG" | sed '1d;$d' | b64url)
```

Assemble the token.

```bash
export TOKEN="exe0.$PAYLOAD.$SIGBLOB"
```

#### Test

Test the token by running a simple command.

```bash
curl -X POST https://exe.dev/exec -H "Authorization: Bearer $TOKEN" -d 'whoami'
```

## Authentication to VMs

[The above](#authentication) shows how to authenticate to the exe.dev API. For programmatic access to VMs, we support something very similar.

Our [HTTPS auth proxy](/docs/proxy) gates access to websites on your VMs, but it assumes a browser and cookies. For API servers or `git push` over HTTPS, you can generate signed bearer tokens that the proxy will respect.

### How it works

VM tokens work just like API tokens, with two differences:

1. **Namespace**: Instead of `v0@exe.dev`, the namespace is `v0@VMNAME.exe.xyz` where `VMNAME` is your VM's name. This scopes the token to a specific VM.

2. **Ctx header**: When a request is authenticated via token, the [`ctx`](#granular-permissions) field from the payload is passed verbatim to your VM's HTTP server in the `X-ExeDev-Token-Ctx` header. The contents are signed, so your server can use them for its own authorization rules.

### Authentication methods

Tokens can be provided in two ways:

- **Bearer token**: Add an `Authorization: Bearer <token>` HTTP header
- **Basic auth**: Username is ignored; password is the token. This works with tools like `git` that use basic auth for HTTPS.

### What your server receives

When a request is authenticated via token, your server receives these headers:

- `X-ExeDev-UserID`: Your exe.dev user ID
- `X-ExeDev-Email`: Your email address
- `X-ExeDev-Token-Ctx`: The `ctx` field from the token, passed verbatim (if present)

### Using with git

For git HTTPS access, save the token to a file and configure git to supply it as the password via basic auth.

```bash
echo "$TOKEN" > ~/.ssh/exe_dev_token
```

```bash
git config credential.helper '!f() { echo "password=$(cat ~/.ssh/exe_dev_token)"; }; f'
```

```bash
git clone https://myvm.exe.xyz/repo.git
```

## Token details

### Granular permissions

Token permissions are specified using (signed) JSON. The empty object `{}` gives you the defaults, and each field you add overrides a default.

The permissions JSON is public and embedded as plaintext in your token. Do not put secrets in it.

Available fields:

- `exp`: specifies an integer UTC unix timestamp after which the token is no longer valid. For example, `{"exp":1922918400}` means this token cannot be used after Dec 5, 2030. The default `exp` is the distant future, that is, it never expires. We strongly recommend always setting `exp`.

- `nbf`: specifies a UTC unix timestamp before which the token is not yet valid. For example, `{"nbf": 1922918400}` means this token cannot be used until Dec 5, 2030. The default `nbf` is the distant past.

- `cmds`: specifies which exe.dev commands this token can execute. Subcommands are specified as a single string, such as `"ssh-key list"`. Including a parent command like `"ssh-key"` does _not_ grant access to its subcommands. Flags, arguments, and options (like `--json`) are always allowed when the base command is permitted; `cmds` controls command names only. The default `cmds` is `["help","ls","new","whoami","ssh-key list","share show"]`.

- `ctx`: uninterpreted by exe.dev. Can be used to differentiate otherwise-identical tokens, or to pass data to your VM server (see [Authentication to VMs](#authentication-to-vms)). Must contain valid JSON that complies with the restrictions in the next section.

Need a new type of permission? Let us know: [support@exe.dev](mailto:support@exe.dev) or [Discord](https://discord.gg/jc9WQUfaxf).

### JSON recommendations and restrictions

We recommend compacting the JSON to keep tokens short: remove all whitespace, or pipe through `jq -c`.

There are a few JSON restrictions, including inside `ctx`, for good security hygiene.

- No leading or trailing whitespace.
- No newlines (`\n`, `\r`).
- No null bytes.
- No duplicate keys, at any level.
- Known fields only: Only `exp`, `nbf`, `cmds`, and `ctx` are allowed at the top level.
- Integers: `exp` and `nbf` must be integers. No decimals like `2000000000.0`, no exponents like `2e9`.
- Timestamp range: `exp` and `nbf` must be between Jan 1, 2000 (946684800) and Jan 1, 2100 (4102444800).
- Size limit: The entire token must not exceed 8KB.

The `ctx` field is passed through to your server verbatim, but we do validate its internal structure against these rules.

## Troubleshooting

### Invalid token (401)

The token is malformed, expired, signed with an unrecognized key, or the signature doesn't verify. Common causes:

- The key used to sign the token hasn't been added to your exe.dev account. Run `ssh exe.dev ssh-key list` to check.
- The token has expired (`exp` is in the past).
- Whitespace or newlines in the permissions JSON. The payload must be byte-for-byte identical to what was signed. Pipe through `jq -c` to compact, and avoid editors that add trailing newlines.
- Using `ssh-agent` instead of a key file. `ssh-keygen -Y sign` requires `-f path/to/key`. If your key is only in the agent, export it first: `ssh-add -L | grep "your-key-comment" > /tmp/key.pub`, then use the private key file directly.

### Bad request (400)

The request body is empty, missing, or has invalid command syntax (e.g., unbalanced quotes).

### Command not allowed by token permissions (403)

The token's `cmds` list doesn't include the command you're trying to run. The token payload is base64url-encoded and can be decoded to inspect its contents.

Subcommands must be listed explicitly. Including `"ssh-key"` does _not_ grant access to `"ssh-key list"`.

### Unknown command (404)

The command doesn't exist. Check `ssh exe.dev help` for the full list of available commands.

### Method not allowed (405)

Only POST is accepted. You sent a GET, PUT, or other HTTP method.

### Request too large (413)

The request body exceeds the 64KB limit.

### Command failed (422)

The command ran but returned a non-zero exit code (e.g., missing arguments, invalid input). The body contains the error message.

### Timeout (504)

The command took longer than 30 seconds to execute.

### Internal error (500)

Something unexpected went wrong server-side. If this persists, contact [support@exe.dev](mailto:support@exe.dev).

## FAQ

**Is there replay protection?** There is no built-in nonce or `jti` mechanism. Use short-lived tokens (small `exp`) to limit the replay window. Use separate ssh keys for sets of API keys for revocability.

**What are the /exec limitations?** The API has no stdin, no pty, and a 30-second timeout (HTTP 504 on timeout). Commands that require interactive input won't work. If it hurts, don't do it. The request body limit is 64KB.
