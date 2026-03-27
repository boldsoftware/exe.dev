"""Generate GitHub App installation tokens for Buildkite CI steps.

Usage:
    import github_token
    token = github_token.get()
    # or from the command line:
    # python3 .buildkite/steps/github_token.py
"""

import base64
import json
import os
import subprocess
import sys
import tempfile
import time

APP_CLIENT_ID = "Iv23liu81FFLPs0w9AO8"
SECRET_NAME = "EXE_COMMIT_QUEUE_APP_PRIVATE_KEY"
GITHUB_ORG = "boldsoftware"
REPOS = ["exe", "shelley", "exeuntu", "exe.dev"]


def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def _generate_jwt(pem: str) -> str:
    now = int(time.time())
    header = _b64url(json.dumps({"typ": "JWT", "alg": "RS256"}).encode())
    payload = _b64url(json.dumps({
        "iat": now - 60,
        "exp": now + 600,
        "iss": APP_CLIENT_ID,
    }).encode())
    signing_input = f"{header}.{payload}"

    with tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as f:
        f.write(pem)
        f.flush()
        proc = subprocess.run(
            ["openssl", "dgst", "-sha256", "-sign", f.name],
            input=signing_input.encode(),
            capture_output=True,
        )
        os.unlink(f.name)
    if proc.returncode != 0:
        raise RuntimeError(f"openssl sign failed: {proc.stderr.decode()}")
    return f"{header}.{payload}.{_b64url(proc.stdout)}"


def _get_installation_token(jwt: str) -> str:
    r = subprocess.run(
        ["curl", "-sS",
         "-H", "Accept: application/vnd.github+json",
         "-H", f"Authorization: Bearer {jwt}",
         "-H", "X-GitHub-Api-Version: 2022-11-28",
         "https://api.github.com/app/installations"],
        capture_output=True, text=True, check=True,
    )
    installations = json.loads(r.stdout)
    installation_id = None
    for inst in installations:
        if inst.get("account", {}).get("login") == GITHUB_ORG:
            installation_id = inst["id"]
            break
    if not installation_id:
        raise RuntimeError(f"No installation found for org {GITHUB_ORG}")

    r = subprocess.run(
        ["curl", "-sS", "-X", "POST",
         "-H", "Accept: application/vnd.github+json",
         "-H", f"Authorization: Bearer {jwt}",
         "-H", "X-GitHub-Api-Version: 2022-11-28",
         f"https://api.github.com/app/installations/{installation_id}/access_tokens",
         "-d", json.dumps({"repositories": REPOS})],
        capture_output=True, text=True, check=True,
    )
    resp = json.loads(r.stdout)
    token = resp.get("token")
    if not token:
        raise RuntimeError(f"Failed to get token: {r.stdout}")
    return token


def get() -> str:
    """Read the GitHub App private key from Buildkite secrets and return an installation token."""
    pem = subprocess.run(
        ["buildkite-agent", "secret", "get", SECRET_NAME],
        capture_output=True, text=True,
    ).stdout.strip()
    if not pem:
        raise RuntimeError("Could not read GitHub App private key from Buildkite secrets")
    jwt = _generate_jwt(pem)
    return _get_installation_token(jwt)


def configure_origin(token: str):
    """Set the origin remote URL to use the given token."""
    url = f"https://x-access-token:{token}@github.com/{GITHUB_ORG}/exe.git"
    subprocess.run(["git", "remote", "set-url", "origin", url], check=True)


if __name__ == "__main__":
    print(get())
