#!/bin/bash
set -euo pipefail

OUT="type=local,dest=pkg"

# retry runs a command up to N times, riding over transient failures such as
# Docker Hub returning a 502 while resolving the golang base image. Backoff
# grows with each attempt.
retry() {
    local attempts="$1"
    shift
    local n=1
    until "$@"; do
        if [[ "${n}" -ge "${attempts}" ]]; then
            return 1
        fi
        echo "protos: command failed (attempt ${n}/${attempts}); retrying in $((n * 5))s..." >&2
        sleep "$((n * 5))"
        n=$((n + 1))
    done
}

# chown_back restores ownership of the generated files to the invoking user,
# when using DOCKER="sudo docker".
chown_back() {
    [[ $# -gt 0 ]] || return 0
    [[ -n "$(find pkg/api ! -user "$(id -u)" -print -quit 2>/dev/null)" ]] || return 0
    "$@" chown -R "$(id -u):$(id -g)" pkg/api
}

build_with_docker() {
    local -a cmd=()
    read -r -a cmd <<<"${DOCKER:-docker}"
    if [[ ${#cmd[@]} -eq 0 ]] || ! command -v "${cmd[0]}" >/dev/null 2>&1; then
        return 1
    fi
    "${cmd[@]}" info >/dev/null 2>&1 || return 1
    "${cmd[@]}" buildx version >/dev/null 2>&1 || return 1
    # The privilege prefix, if the caller asked for one (DOCKER="sudo docker").
    local -a priv=()
    case "$(basename "${cmd[0]}")" in
    sudo | doas) priv=("${cmd[0]}") ;;
    esac
    # --pull=false reuses the locally cached base image instead of forcing a
    # refresh of the floating golang:latest tag against Docker Hub on every
    # build. A busy CI box already has the image; a flaky registry must not
    # fail a build whose base image is present. (A cold cache still fetches.)
    local status=0
    retry 3 "${cmd[@]}" buildx build --pull=false -f ./Dockerfile.protobuf --output "${OUT}" . || status=$?
    chown_back "${priv[@]+"${priv[@]}"}"
    exit "${status}"
}

# build_with_lima_nerdctl builds inside the Lima VM named "default", which ships
# containerd/nerdctl. The build output lands inside the VM, so copy it back to
# pkg/. This gives macOS developers a code-generation path without Docker.
build_with_lima_nerdctl() {
    command -v limactl >/dev/null 2>&1 || return 1
    limactl shell default -- nerdctl version >/dev/null 2>&1 || return 1

    local tmp status=0
    tmp="$(limactl shell default -- mktemp -d)" || return 1
    retry 3 limactl shell default -- nerdctl build -f ./Dockerfile.protobuf --output "type=local,dest=${tmp}" . || status=$?
    if [[ "${status}" -eq 0 ]]; then
        mkdir -p pkg && limactl cp -r "default:${tmp}/." pkg/ || status=$?
    fi
    limactl shell default -- rm -rf "${tmp}" >/dev/null 2>&1 || true
    exit "${status}"
}

build_with_docker || build_with_lima_nerdctl || {
    echo "protos: need Docker buildx (set \$DOCKER to override the docker command) or a Lima VM named default" >&2
    exit 1
}
