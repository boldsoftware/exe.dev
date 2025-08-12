#!/bin/bash
# Manual Cloud NAT setup to fix container internet access
# Run this with appropriate GCP permissions (project owner or network admin)

set -e

PROJECT_ID="exe-dev-468515"
REGION="us-west2"

echo "Setting up Cloud NAT for GKE cluster internet access..."
echo "This requires compute.routers.create and compute.routers.update permissions"
echo ""

# Create Cloud Router
echo "Creating Cloud Router..."
gcloud compute routers create exe-nat-router \
    --network=default \
    --region=$REGION \
    --project=$PROJECT_ID \
    || echo "Cloud Router may already exist, continuing..."

# Create Cloud NAT
echo "Creating Cloud NAT configuration..."
gcloud compute routers nats create exe-nat-config \
    --router=exe-nat-router \
    --region=$REGION \
    --nat-all-subnet-ip-ranges \
    --auto-allocate-nat-external-ips \
    --project=$PROJECT_ID \
    || echo "Cloud NAT may already exist"

echo ""
echo "Cloud NAT setup complete!"
echo "Pods in the GKE cluster should now be able to access the internet."
echo "It may take 1-2 minutes for the NAT to become fully operational."