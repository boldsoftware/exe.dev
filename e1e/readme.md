# e1e Tests

These are end-to-end tests.

They're called e1e because e2e was already taken when they were created.

They spin up the full stack (exed, piperd) and a few external services (a fake
email server) and then interact with the system the way a user would.

For debugging, run with -v and look at the instructions it prints at the top.
Run with "-vv" for extra more logging.

The e1e tests generate two forms of recordings:

- *.cast files, which are asciinema recordings of what the user sees. You can play them locally with `asciinema play TestFoo.cast`.
  In CI, these are bundled into a standalone HTML viewer that is made available as an artifact.

- golden/*.txt files, see e1e/golden/readme.md for documentation of them.

# Running e1e Tests

## On macOS (with Lima VM)

Tests automatically detect and use an existing Lima VM:

```bash
go test -v -run TestVanillaBox ./e1e
```

## On Linux

The default behavior uses libvirt to start a VM,
and then uses that as the "ctr-host". However,
if you set CTR_HOST, you can use that host as a VM.
A special case is CTR_HOST=localhost

### CTR_HOST=localhost

Run tests directly on a Linux machine with KVM and ZFS:

```bash
CTR_HOST=localhost go test -v -run TestVanillaBox ./e1e
```

**Requirements:**
- `/dev/kvm` access
- ZFS tools (`apt install zfsutils-linux`)
- udevd running (for zvol symlinks)
- cloud-hypervisor (auto-downloaded if missing)
- ~50GB free disk space in `/tmp` for the ZFS pool sparse file

The bootstrap process automatically:
- Creates a 50GB sparse ZFS pool at `/tmp/tank.img` (actual usage is much smaller due to copy-on-write)
- Installs ZFS tools if missing
- Downloads cloud-hypervisor
- Configures hugepages (~50% of RAM)
- Starts udevd if not running

**Starting udevd (if not running):**
```bash
# On systems with systemd:
sudo systemctl start systemd-udevd

# In containers or minimal environments:
sudo udevd --daemon
```

Image caches (`tank/sha256:*`) are preserved between runs for faster iteration.
Note: The cache must be seeded manually for now (see TODO in testinfra/exelet.go).
