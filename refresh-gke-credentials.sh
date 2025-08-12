#!/bin/bash
# Script to refresh GKE credentials periodically
# This prevents token expiration issues

set -e

CLUSTER_NAME="exe-cluster"
ZONE="us-west2-a"
PROJECT_ID="exe-dev-468515"
USER="ubuntu"

echo "$(date): Refreshing GKE credentials..."

# Refresh the credentials
sudo -u $USER gcloud container clusters get-credentials $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID \
    --quiet

# Test that the credentials work
if sudo -u $USER kubectl get nodes --request-timeout=5s > /dev/null 2>&1; then
    echo "$(date): Credentials refreshed successfully"
    # Restart exed to pick up new credentials
    systemctl restart exed
    echo "$(date): exed service restarted"
else
    echo "$(date): Failed to refresh credentials"
    exit 1
fi