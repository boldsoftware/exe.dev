# execonnect

`execonnect` connects services on a private network to your exe.dev VMs over
an outbound-only tunnel.

**See the [External Connections guide](https://exe.dev/docs/integrations-external-connections)
for setup and usage.**

## Install

```sh
curl -fsSL https://exe.dev/execonnect/install.sh | sh
```

Releases are tagged `execonnect/v*` on GitHub.

## Build

From this directory:

```sh
make check
make build
```

The binary is written to `bin/execonnect`. To install a local build as a
systemd service:

```sh
sudo ./packaging/systemd/install.sh ./bin/execonnect
sudo systemctl enable --now execonnect
```

## License

Copyright 2026 Bold Software, Inc.

Licensed under [Apache-2.0](./LICENSE), except where otherwise noted.
The WireGuard-derived `vpctunnel/nstun/nstun.go` remains under the
[MIT license](./LICENSES/WireGuard-MIT.txt); see [NOTICE](./NOTICE).
