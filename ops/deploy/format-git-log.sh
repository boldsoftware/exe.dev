#!/bin/bash
# Formats git log output with column-aligned author first names.
# Usage: git log --format="%h	%an	%s" ... | ./format-git-log.sh

awk -F'\t' '
BEGIN {
    name["Auto-formatter"] = "Bot"
    name["Blake Mizerany"] = "Blake"
    name["David Crawshaw"] = "David"
    name["Evan Hazlett"] = "Evan"
    name["Exe.dev System"] = "Bot"
    name["GitHub Actions"] = "Bot"
    name["Ian Lance Taylor"] = "Ian"
    name["Josh Bleecher Snyder"] = "Josh"
    name["Philip Zeyliger"] = "Philip"
    name["philip@bold.dev"] = "Philip"
    name["philz"] = "Philip"
    name["Shaun Loo"] = "Shaun"
}
{
    if ($3 ~ /fix formatting/) next
    author = $2
    first = (author in name) ? name[author] : author
    printf "%s  %-6s  %s\n", $1, first, $3
}'
