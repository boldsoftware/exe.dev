#!/bin/bash

echo "🕸️  checking what is deployed..."

DEPLOYED_SHA=$(curl -s https://exed-01.crocodile-vector.ts.net/debug/gitsha)

if [ $? -ne 0 ]; then
	echo "😞 could not get deployed SHA (curl failed)"
	exit 1
fi

if [ -z "$DEPLOYED_SHA" ]; then
	echo "😞 could not get deployed SHA (empty response)"
	exit 1
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
	git log --grep="^devlog" --invert-grep --format="%h %an: %s" "${DEPLOYED_SHA}".."${CURRENT_SHA}"
fi
