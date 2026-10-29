#!/bin/sh
# Download and install the latest execonnect release:
#
#   curl -fsSL https://raw.githubusercontent.com/boldsoftware/exe.dev/main/execonnect/install.sh | sh
#
# Set EXECONNECT_VERSION=v0.N.9xxx to install a specific release.
#
# Linux installs the systemd service (packaging/systemd/install.sh) and
# restarts it if it is running. macOS installs /usr/local/bin/execonnect.
set -eu

# Everything runs from main, called on the last line, so a truncated
# download (curl | sh) executes nothing.

die() {
    echo "execonnect install: $*" >&2
    exit 1
}

main() {
    case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported OS: $(uname -s)" ;;
    esac
    case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
    esac

    sudo=
    if [ "$(id -u)" -ne 0 ]; then
        sudo=sudo
    fi

    version=${EXECONNECT_VERSION:-}
    if [ -z "$version" ]; then
        # The repo releases several components; take the newest execonnect one.
        version=$(curl -fsSL "https://api.github.com/repos/boldsoftware/exe.dev/releases?per_page=100" |
            tr ',' '\n' | sed -n 's/.*"tag_name": *"execonnect\/\(v[^"]*\)".*/\1/p' | head -1)
        [ -n "$version" ] || die "no execonnect release found"
    fi

    if [ "$os" = linux ]; then
        asset=execonnect-linux-$arch.tar.gz
    else
        asset=execonnect-darwin-$arch
    fi

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    cd "$tmp"
    url=https://github.com/boldsoftware/exe.dev/releases/download/execonnect/$version
    echo "Downloading execonnect $version ($os/$arch)"
    curl -fsSL -o "$asset" "$url/$asset"
    curl -fsSL -o checksums.txt "$url/checksums.txt"

    want=$(awk -v a="$asset" '$2 == a { print $1 }' checksums.txt)
    if command -v sha256sum >/dev/null 2>&1; then
        got=$(sha256sum "$asset" | awk '{ print $1 }')
    else
        got=$(shasum -a 256 "$asset" | awk '{ print $1 }')
    fi
    [ -n "$want" ] && [ "$got" = "$want" ] || die "checksum mismatch for $asset"

    if [ "$os" = darwin ]; then
        $sudo install -d /usr/local/bin
        $sudo install -m 0755 "$asset" /usr/local/bin/execonnect
        echo "Installed /usr/local/bin/execonnect; run: execonnect serve"
        return
    fi

    tar -xzf "$asset"
    cd "execonnect-linux-$arch"
    $sudo ./install.sh ./execonnect
    if systemctl is-active --quiet execonnect; then
        $sudo systemctl restart execonnect
        echo "Restarted execonnect"
    fi
}

main "$@"
