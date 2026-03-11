#!/usr/bin/env python3
"""Create a GitHub App for exe.dev using the App Manifest flow.

Usage: python3 ops/create-github-app.py <prod|staging|dev> [--env-file PATH]
"""

import html
import http.server
import json
import os
import secrets
import sys
import threading
import urllib.error
import urllib.request
import webbrowser
from urllib.parse import parse_qs, urlparse

ORG = "boldsoftware"
LISTEN_PORT = 3829

ENVS = {
    "prod": {
        "name": "exe.dev integration",
        "prefix": "EXE_PROD_APP",
        "url": "https://exe.dev",
        "callback_url": "https://exe.dev/github/callback",
    },
    "staging": {
        "name": "exe-staging.dev",
        "prefix": "EXE_STAGING_APP",
        "url": "https://exe-staging.dev",
        "callback_url": "https://exe-staging.dev/github/callback",
    },
    "dev": {
        "name": "exe.dev dev",
        "prefix": "EXE_DEV_APP",
        "url": "http://localhost:8080",
        "callback_url": "http://localhost:8080/github/callback",
    },
}

# Parse args.
env_name = None
env_file = None
args = sys.argv[1:]
i = 0
while i < len(args):
    if args[i] == "--env-file" and i + 1 < len(args):
        env_file = args[i + 1]
        i += 2
    elif env_name is None and args[i] in ENVS:
        env_name = args[i]
        i += 1
    else:
        print(f"Usage: {sys.argv[0]} <prod|staging|dev> [--env-file PATH]", file=sys.stderr)
        sys.exit(1)

if not env_name:
    print(f"Usage: {sys.argv[0]} <prod|staging|dev> [--env-file PATH]", file=sys.stderr)
    sys.exit(1)

env = ENVS[env_name]
prefix = env["prefix"]
state = secrets.token_hex(16)

manifest = json.dumps({
    "name": env["name"],
    "url": env["url"],
    "hook_attributes": {"url": "https://example.com/unused", "active": False},
    "redirect_url": f"http://localhost:{LISTEN_PORT}/callback",
    "callback_urls": [env["callback_url"]],
    "request_oauth_on_install": True,
    "public": True,
    "default_permissions": {
        "contents": "write",
        "actions": "write",
    },
})

# HTML page that auto-submits the manifest form to GitHub.
START_HTML = f"""\
<!DOCTYPE html>
<html>
<head><title>Creating GitHub App: {html.escape(env["name"])}</title></head>
<body>
<h2>Creating GitHub App: {html.escape(env["name"])}</h2>
<p>Submitting to GitHub...</p>
<form id="form" method="post"
      action="https://github.com/organizations/{ORG}/settings/apps/new?state={state}">
  <input type="hidden" name="manifest" value='{html.escape(manifest, quote=True)}'>
  <noscript><button type="submit">Click here if not redirected</button></noscript>
</form>
<script>document.getElementById("form").submit();</script>
</body>
</html>
"""

# Event to signal callback received.
done = threading.Event()
result = {}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path in ("/start", "/start/"):
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(START_HTML.encode())
            return

        if parsed.path in ("/callback", "/callback/"):
            qs = parse_qs(parsed.query)
            code = qs.get("code", [None])[0]
            got_state = qs.get("state", [None])[0]

            if got_state != state:
                self.send_response(403)
                self.end_headers()
                self.wfile.write(b"State mismatch - possible CSRF. Aborting.")
                return

            if not code:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Missing code parameter.")
                return

            result["code"] = code
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(b"<h2>Done! You can close this tab.</h2>")
            done.set()
            return

        self.send_response(404)
        self.end_headers()

    def log_message(self, fmt, *args):
        pass  # silence request logs


server = http.server.HTTPServer(("127.0.0.1", LISTEN_PORT), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()

url = f"http://localhost:{LISTEN_PORT}/start"
print(f"Opening browser to {url}")
if not webbrowser.open(url):
    print(f"Could not open browser. Navigate manually to:\n  {url}")

print("Waiting for GitHub callback (5 min timeout)...")
if not done.wait(timeout=300):
    print("ERROR: Timed out waiting for GitHub callback.", file=sys.stderr)
    server.shutdown()
    sys.exit(1)

server.shutdown()

# Exchange the code for app credentials.
code = result["code"]
req = urllib.request.Request(
    f"https://api.github.com/app-manifests/{code}/conversions",
    method="POST",
    headers={
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    },
)

try:
    with urllib.request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read())
except urllib.error.HTTPError as exc:
    body = exc.read().decode(errors="replace")
    print(f"ERROR: GitHub API returned {exc.code}: {body}", file=sys.stderr)
    sys.exit(1)

# Build env var lines.
lines = [
    f'{prefix}_GITHUB_APP_ID={data["id"]}',
    f'{prefix}_GITHUB_CLIENT_ID={data["client_id"]}',
    f'{prefix}_GITHUB_CLIENT_SECRET={data["client_secret"]}',
    f'{prefix}_GITHUB_WEBHOOK_SECRET={data["webhook_secret"]}',
    f'{prefix}_GITHUB_PRIVATE_KEY="{data["pem"]}"',
]

print()
for line in lines:
    print(line)

if env_file:
    with open(env_file, "a") as f:
        f.write("\n")
        for line in lines:
            f.write(line + "\n")
    print(f"\nAppended to {env_file}")

slug = data.get("slug", data.get("name", "").lower().replace(" ", "-"))
print(f"""
NOTE: Enable device flow manually at:
  https://github.com/organizations/{ORG}/settings/apps/{slug}

  Under "Identifying and authorizing users", check "Enable Device Flow"
""")
