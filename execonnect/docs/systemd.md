# systemd installation

This installation path supports Linux systems running systemd with GNU shadow's
`useradd` and `groupadd`. Alpine/BusyBox account tools are not supported.
macOS uses foreground `execonnect serve` instead.

## Build and install

From the standalone `execonnect` module root:

```sh
make check
make build
sudo ./packaging/systemd/install.sh ./bin/execonnect
```

The installer validates its sibling unit, input binary, root privileges,
required commands, and existing `execonnect` account state before changing
anything. It then:

- creates the static `execonnect` group and user when absent;
- installs the binary at `/usr/local/bin/execonnect`;
- installs the unit at `/etc/systemd/system/execonnect.service`; and
- runs `systemctl daemon-reload`.

It does not enable, start, restart, or enroll the service.

## Start and enroll

```sh
sudo systemctl enable --now execonnect
execonnect status
execonnect enroll
execonnect endpoint add NAME URL
```

The daemon keeps state in `/var/lib/execonnect` and listens on
`/run/execonnect/control.sock`. The runtime directory and socket authorize the
`execonnect` group. Group members can inspect status, enroll or replace the
connector identity, and change every endpoint, so add only trusted operators:

```sh
sudo usermod --append --groups execonnect OPERATOR
```

Start a new login session after changing group membership.

## Upgrade

Build or obtain the replacement binary, rerun the installer, then restart:

```sh
sudo ./packaging/systemd/install.sh ./bin/execonnect
sudo systemctl restart execonnect
execonnect status
```

## Troubleshooting

```sh
execonnect status
sudo systemctl status execonnect
sudo journalctl --unit execonnect --follow
```

If an unflagged non-root client has no usable foreground-user socket, it also
checks the system socket. `--socket` always selects one exact socket;
`--state-dir` selects that directory's `control.sock` unless `--socket` is set.

## Uninstall and purge

Stop and uninstall the service before removing any state:

```sh
sudo systemctl disable --now execonnect
sudo rm -f /etc/systemd/system/execonnect.service /usr/local/bin/execonnect
sudo systemctl daemon-reload
```

This preserves the static account and `/var/lib/execonnect`, so reinstalling can
reuse the connector identity and endpoint configuration.

Purging is irreversible. Only after the service is stopped and uninstalled,
remove the state and account:

```sh
sudo rm -rf /var/lib/execonnect
sudo userdel execonnect
sudo groupdel execonnect
```

Purging destroys the connector secret, WireGuard identity, and endpoint
configuration. Create or enroll a replacement External Connection before using
the connector again.
