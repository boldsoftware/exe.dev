#!/bin/bash
# Verify that the deploy target matches the expected environment.
# Catches the mistake of running a prod script against a staging host or vice versa.
#
# Usage: check-deploy-env.sh <prod|staging> <instance-name>

set -e

EXPECTED_ENV="$1"
INSTANCE_NAME="$2"

if [ -z "$EXPECTED_ENV" ] || [ -z "$INSTANCE_NAME" ]; then
    echo "ERROR: Usage: check-deploy-env.sh <prod|staging> <instance-name>" >&2
    exit 1
fi

case "$EXPECTED_ENV" in
    staging)
        if [[ "$INSTANCE_NAME" != *staging* ]]; then
            echo "ERROR: Instance name '$INSTANCE_NAME' does not contain 'staging'." >&2
            echo "       This is a staging deploy script — refusing to deploy to a non-staging host." >&2
            exit 1
        fi
        ;;
    prod)
        if [[ "$INSTANCE_NAME" == *staging* ]]; then
            echo "ERROR: Instance name '$INSTANCE_NAME' contains 'staging'." >&2
            echo "       This is a prod deploy script — refusing to deploy to a staging host." >&2
            exit 1
        fi
        ;;
    *)
        echo "ERROR: Expected environment must be 'prod' or 'staging', got '$EXPECTED_ENV'" >&2
        exit 1
        ;;
esac
