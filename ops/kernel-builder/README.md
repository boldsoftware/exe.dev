# Kernel Builder

This directory builds a customized kernel usable by Kata/Cloud Hypervisor.
We re-use Kata's build system, but change kernel flags.

The original motivation here was to enable CONFIG_NF_TABLES so as to
allow Docker to work inside our VMs.

## Build

```bash
./build.sh
```

Outputs to `output/vmlinux-6.12.42-nftables`

## Deploy

Copy to `/opt/kata/share/kata-containers/` and update the `vmlinux.container` symlink:

```bash
sudo cp output/vmlinux-6.12.42-nftables /opt/kata/share/kata-containers/
sudo ln -sf vmlinux-6.12.42-nftables /opt/kata/share/kata-containers/vmlinux.container
```

## Testing

Docker works in Kata containers with this kernel:

```bash
# Create container with proper flags
sudo nerdctl --namespace exe --snapshotter nydus run -d \
  --runtime io.containerd.kata.v2 \
  --cap-add=ALL --cgroupns private \
  --tmpfs /sys/fs/cgroup:rw \
  ubuntu:24.04 sleep infinity

# Install and run Docker
nerdctl exec <container> sh -c '
  apt-get update && apt-get install -y docker.io
  mount -t cgroup2 none /sys/fs/cgroup
  dockerd --iptables=false --ip-forward=false &
  sleep 10
  docker run --rm --network=host hello-world
'
```

## References

- https://github.com/cloud-hypervisor/cloud-hypervisor/issues/7404
- https://github.com/kata-containers/kata-containers/tree/main/tools/packaging/kernel
