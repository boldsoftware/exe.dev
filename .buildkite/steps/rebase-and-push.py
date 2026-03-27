#!/usr/bin/env python3
"""Rebase the current branch onto origin/main and push.

All tests have passed at this point. We:
  1. Generate a GitHub App installation token
  2. Configure git to use it
  3. Rebase onto origin/main
  4. Push to main
"""

import base64
import json
import os
import subprocess
import sys
import tempfile
import time

# GitHub App config
APP_CLIENT_ID = "Iv23liu81FFLPs0w9AO8"
SECRET_NAME = "EXE_COMMIT_QUEUE_APP_PRIVATE_KEY"
GITHUB_ORG = "boldsoftware"
GITHUB_REPO = "exe"


def run(*cmd, check=True, capture=False):
    print(f"+ {' '.join(cmd)}", flush=True)
    if capture:
        return subprocess.run(cmd, check=check, capture_output=True, text=True)
    return subprocess.run(cmd, check=check)


def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def generate_jwt(pem: str) -> str:
    """Generate a JWT signed with the GitHub App's private key."""
    now = int(time.time())
    header = b64url(json.dumps({"typ": "JWT", "alg": "RS256"}).encode())
    payload = b64url(json.dumps({
        "iat": now - 60,
        "exp": now + 600,
        "iss": APP_CLIENT_ID,
    }).encode())
    signing_input = f"{header}.{payload}"

    # Use openssl to sign (available on CI agents)
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
    signature = b64url(proc.stdout)
    return f"{header}.{payload}.{signature}"


def get_installation_token(jwt: str) -> str:
    """Exchange JWT for an installation access token."""
    # Find the installation ID for our org
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
        print(f"Available installations: {json.dumps(installations, indent=2)}", file=sys.stderr)
        raise RuntimeError(f"No installation found for org {GITHUB_ORG}")

    print(f"Installation ID: {installation_id}", flush=True)

    # Request a token scoped to our repo
    r = subprocess.run(
        ["curl", "-sS", "-X", "POST",
         "-H", "Accept: application/vnd.github+json",
         "-H", f"Authorization: Bearer {jwt}",
         "-H", "X-GitHub-Api-Version: 2022-11-28",
         f"https://api.github.com/app/installations/{installation_id}/access_tokens",
         "-d", json.dumps({"repositories": [GITHUB_REPO]})],
        capture_output=True, text=True, check=True,
    )
    resp = json.loads(r.stdout)
    token = resp.get("token")
    if not token:
        raise RuntimeError(f"Failed to get token: {r.stdout}")
    return token


def main():
    print("--- :key: Generate GitHub App token", flush=True)
    pem = run("buildkite-agent", "secret", "get", SECRET_NAME, capture=True).stdout
    if not pem.strip():
        print("ERROR: Could not read private key from Buildkite secrets", file=sys.stderr)
        sys.exit(1)

    jwt = generate_jwt(pem)
    token = get_installation_token(jwt)
    print("Token acquired.", flush=True)

    # Configure git to use the token
    repo_url = f"https://x-access-token:{token}@github.com/{GITHUB_ORG}/{GITHUB_REPO}.git"
    run("git", "remote", "set-url", "origin", repo_url)

    print("--- :git: Rebase onto origin/main", flush=True)
    run("git", "config", "user.email", "ci@exe.dev")
    run("git", "config", "user.name", "exe CI")
    run("git", "fetch", "origin", "main")

    head_sha = run("git", "rev-parse", "--short", "HEAD", capture=True).stdout.strip()
    main_sha = run("git", "rev-parse", "--short", "origin/main", capture=True).stdout.strip()
    print(f"HEAD: {head_sha}, origin/main: {main_sha}", flush=True)

    result = run("git", "rebase", "origin/main", check=False)
    if result.returncode != 0:
        run("git", "rebase", "--abort", check=False)
        print("ERROR: Rebase failed. Rebase locally and re-push.", file=sys.stderr)
        sys.exit(1)

    new_sha = run("git", "rev-parse", "--short", "HEAD", capture=True).stdout.strip()
    print(f"Rebased: {head_sha} -> {new_sha} (on {main_sha})", flush=True)

    print("--- :rocket: Push to origin/main", flush=True)
    result = run("git", "push", "origin", "HEAD:refs/heads/main", check=False)
    if result.returncode != 0:
        print("ERROR: Push failed. Someone may have pushed in the meantime.", file=sys.stderr)
        sys.exit(1)

    print("Successfully pushed to main!", flush=True)


if __name__ == "__main__":
    main()
