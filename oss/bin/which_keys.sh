#!/bin/bash

# which_keys.sh attempts to find the private and public SSH key files
# that your system uses by default to ssh to exe.dev.
#
# If you are doing something fancy, like using an SSH agent,
# this script may not be able to find your keys.

set -euo pipefail

# Get the fingerprint of the current SSH key from the server.
fingerprint="$(ssh exe.dev whoami --json | jq -r '.ssh_keys[] | select(.current) | .fingerprint')"

if [ -z "$fingerprint" ]; then
    echo "error: could not get fingerprint from server" >&2
    exit 1
fi

# Get all identity files from SSH config for exe.dev.
identity_files="$(ssh -G exe.dev | awk '$1=="identityfile"{print $2}')"

# Check each identity file to find the one matching the fingerprint.
while IFS= read -r key_path; do
    # Expand ~ to home directory.
    key_path="${key_path/#\~/$HOME}"

    # Skip if file doesn't exist.
    if [ ! -f "$key_path" ]; then
        continue
    fi

    # Get the fingerprint of this key.
    # ssh-keygen -lf outputs: "256 SHA256:xxxx comment (ED25519)"
    key_fingerprint="$(ssh-keygen -E sha256 -lf "$key_path" 2>/dev/null | awk '{print $2}')" || continue

    if [ "$key_fingerprint" = "$fingerprint" ]; then
        echo "private: $key_path"
        if [ -f "$key_path.pub" ]; then
            echo "public:  $key_path.pub"
        fi
        exit 0
    fi
done <<<"$identity_files"

echo "error: no matching private key for $fingerprint" >&2
exit 1
