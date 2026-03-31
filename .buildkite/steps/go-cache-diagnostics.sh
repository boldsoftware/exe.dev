#!/usr/bin/env bash
# Prints Go build cache diagnostics. Source this file then call go_cache_stats.
# Usage: source .buildkite/steps/go-cache-diagnostics.sh
#        go_cache_stats "before"  # prints cache size/entry counts
#        ...build...
#        go_cache_stats "after"

go_cache_stats() {
    local label="$1"
    local gocache gomodcache
    gocache=$(go env GOCACHE)
    gomodcache=$(go env GOMODCACHE)
    echo ""
    echo "  [$label] Go cache diagnostics:"
    echo "  GOCACHE=$gocache"
    echo "  GOMODCACHE=$gomodcache"
    for pair in "GOCACHE:$gocache" "GOMODCACHE:$gomodcache"; do
        local name="${pair%%:*}"
        local path="${pair#*:}"
        if [ -d "$path" ]; then
            local size count
            size=$(du -sh "$path" 2>/dev/null | cut -f1)
            count=$(find "$path" -maxdepth 2 -type f 2>/dev/null | wc -l)
            echo "  $name: ${size} (${count} entries at depth≤2)"
        else
            echo "  $name: DOES NOT EXIST"
        fi
    done
    echo ""
}
