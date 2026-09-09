# execonnect

`execonnect` connects private-network services to authorized exe.dev workloads
using outbound-only tunnels.

## Check and build

From this repository's `execonnect/` directory:

```sh
make check
make build
```

The binary is written to `bin/execonnect`.

## Run without root

Create an External Connection at
<https://exe.dev/integrations#external-connections>, then start the daemon:

```sh
bin/execonnect serve
```

In another terminal, enroll it and add a private endpoint:

```sh
bin/execonnect enroll
bin/execonnect endpoint add database tcp://db.internal.example:5432
bin/execonnect status
```

On non-root Linux, state uses `$XDG_STATE_HOME/execonnect` (or
`$HOME/.local/state/execonnect`) and the socket uses
`$XDG_RUNTIME_DIR/execonnect/control.sock` (or the state directory). macOS keeps
both under `~/Library/Application Support/execonnect`.

## Install with systemd

```sh
sudo ./packaging/systemd/install.sh ./bin/execonnect
sudo systemctl enable --now execonnect
```

See [systemd installation](./docs/systemd.md) for supported systems, operator
access, upgrades, troubleshooting, uninstall, and purge procedures.

## License

Copyright 2026 Bold Software, Inc.

Licensed under [Apache-2.0](./LICENSE), except where otherwise noted.
The WireGuard-derived `vpctunnel/nstun/nstun.go` remains under the
[MIT license](./LICENSES/WireGuard-MIT.txt); see [NOTICE](./NOTICE).
