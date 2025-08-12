# GKE Authentication for Production

## How Authentication Works

The production VM authenticates to GKE using a **service account**, not user tokens. This ensures the authentication never expires and the service can run indefinitely.

### Authentication Chain

1. **VM Service Account**: The VM runs with service account `exed-vm@exe-dev-468515.iam.gserviceaccount.com`
2. **Metadata Service**: Google's metadata service automatically provides fresh credentials to the VM
3. **gke-gcloud-auth-plugin**: This plugin uses the VM's service account credentials to authenticate to GKE
4. **Automatic Refresh**: Credentials are automatically refreshed by the metadata service (no expiration)

### Key Components

- **Service Account**: `exed-vm@exe-dev-468515.iam.gserviceaccount.com`
- **IAM Role**: `roles/container.developer` (can manage pods, services, etc.)
- **Scope**: `https://www.googleapis.com/auth/cloud-platform` (full access)
- **Auth Plugin**: `gke-gcloud-auth-plugin` (replaces deprecated auth methods)

### No Token Expiration

Unlike user authentication which uses OAuth tokens that expire, the VM's service account authentication:
- Uses the GCE metadata service for credentials
- Automatically refreshes credentials in the background
- Never requires manual intervention
- Will continue working indefinitely as long as the service account exists

### Verification

To verify authentication is working correctly:

```bash
# Check if using service account (should show the service account email)
gcloud auth list

# Test GKE access
kubectl get nodes

# Check logs for successful connection
sudo journalctl -u exed | grep "Successfully connected to Kubernetes cluster"
```

### Troubleshooting

If authentication fails:

1. **Check service account**: `gcloud iam service-accounts describe exed-vm@exe-dev-468515.iam.gserviceaccount.com`
2. **Check IAM permissions**: `gcloud projects get-iam-policy exe-dev-468515 --filter="bindings.members:serviceAccount:exed-vm*"`
3. **Check VM metadata**: `curl -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/email`
4. **Reinstall auth plugin**: `sudo ./fix-gke-auth.sh`