#!/usr/bin/env bash
#
# If tests or whatever leave containers hanging, everything slows to a crawl. This deletes containers
# on the tests machine that were started over an hour ago.
#
# "ssh lima-exe-ctr-tests sudo nerdctl --namespace exe ps" is to see what's up manually; this script
# captures the jq query.

ssh lima-exe-ctr-tests "sudo nerdctl --namespace exe ps --format=json | jq -r 'select((now - (.CreatedAt | sub(\"\\\\.\\\\d+Z$\"; \"Z\") | fromdateiso8601)) > 3600) | .ID' | xargs -r sudo nerdctl --namespace exe rm -f"
