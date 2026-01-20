#!/bin/bash

get_gitsha() {
    local host=$1
    curl -s "http://${host}.crocodile-vector.ts.net:9081/debug/gitsha"
}

echo "🕸️  checking what is deployed..."

if [ -n "$1" ]; then
    # Single host provided
    DEPLOYED_SHA=$(get_gitsha "$1")
    if [ $? -ne 0 ]; then
        echo "😞 could not get deployed SHA from $1 (curl failed)"
        exit 1
    fi
    if [ -z "$DEPLOYED_SHA" ]; then
        echo "😞 could not get deployed SHA from $1 (empty response)"
        exit 1
    fi
else
    # Query all exelets
    HOSTS=$(ssh exe.dev exelets --json | jq -r '.exelets[].host')
    if [ -z "$HOSTS" ]; then
        echo "😞 could not get exelet hosts"
        exit 1
    fi

    HOST_SHAS=""
    UNIQUE_SHAS=""

    for host in $HOSTS; do
        sha=$(get_gitsha "$host")
        if [ -z "$sha" ]; then
            echo "😞 could not get deployed SHA from $host (empty response)"
            exit 1
        fi
        HOST_SHAS="$HOST_SHAS$host:$sha"$'\n'
        if [[ ! " $UNIQUE_SHAS " =~ " $sha " ]]; then
            UNIQUE_SHAS="$UNIQUE_SHAS $sha"
        fi
    done

    # Check if all SHAs are the same
    UNIQUE_COUNT=$(echo $UNIQUE_SHAS | wc -w | tr -d ' ')
    if [ "$UNIQUE_COUNT" -ne 1 ]; then
        echo "😞 exelets have different versions deployed:"
        echo "$HOST_SHAS" | while IFS=: read -r host sha; do
            [ -n "$host" ] && echo "  $host: $sha"
        done
        exit 1
    fi

    DEPLOYED_SHA=$(echo $UNIQUE_SHAS | tr -d ' ')
fi

if ! git rev-parse --quiet --verify "$DEPLOYED_SHA" >/dev/null; then
    echo "😞 could not get deployed SHA (invalid SHA)"
    echo "  $DEPLOYED_SHA"
    exit 1
fi

CURRENT_SHA=$(git rev-parse HEAD)

if [ "$DEPLOYED_SHA" = "$CURRENT_SHA" ]; then
    echo "✅ already deployed: $DEPLOYED_SHA"
else
    echo "🦦 commits that would be deployed (excluding devlog commits):"
    git log --grep="^devlog" --invert-grep --format="%h	%an	%s" "${DEPLOYED_SHA}".."${CURRENT_SHA}" |
        "$(dirname "$0")/format-git-log.sh"
fi
