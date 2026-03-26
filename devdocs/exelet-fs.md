# Building exelet-fs

The exelet needs kernel and rovol filesystem images. These are stored in Backblaze and downloaded automatically by `make exelet-fs` (called by `make exelet`). Downloads are cached and only re-fetched when `exelet/kernel/` or `exelet/rovol/` change.

To rebuild from scratch:

```
# Build on native amd64 + arm64 GitHub Actions runners
gh workflow run build-exelet-fs.yml

# Download the artifacts
make download-exelet-fs-gh

# (Optional) Upload to Backblaze so CI and other devs get them automatically
# Requires B2_APPLICATION_KEY_ID and B2_APPLICATION_KEY with write access
make upload-exelet-fs
```

See `.github/workflows/build-exelet-fs.yml` for the build details.
