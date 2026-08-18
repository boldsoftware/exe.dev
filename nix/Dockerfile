# syntax=docker/dockerfile:1.7

# Build a real NixOS userspace closure, then turn its system tarball into an OCI
# image. exe.dev supplies the kernel and runs /exe.dev/bin/exe-init before /init.
FROM docker.io/nixos/nix:2.35.2 AS builder

ARG NIXPKGS_REV=0dd31db7e6dbf9ce05697c4545f6fe01accec994

RUN mkdir -p /etc/nixos
COPY configuration.nix /etc/nixos/configuration.nix

RUN nix-build \
      --option sandbox false \
      -I "nixpkgs=https://github.com/NixOS/nixpkgs/archive/${NIXPKGS_REV}.tar.gz" \
      -I nixos-config=/etc/nixos/configuration.nix \
      '<nixpkgs/nixos>' \
      -A config.system.build.tarball \
      -o /tmp/nixos-system

RUN mkdir -p /rootfs && \
    xz_bin=$(find /nix/store -path '*/bin/xz' -type f -print -quit) && \
    test -x "$xz_bin" && \
    tar --use-compress-program="$xz_bin" -xf /tmp/nixos-system/tarball/nixos-system-*.tar.xz -C /rootfs && \
    mkdir -p /rootfs/bin /rootfs/etc/nixos /rootfs/home/exedev /rootfs/root && \
    exe_shell=$(find /rootfs/nix/store -path '*/bin/exe-shell' -type f -print -quit) && \
    test -n "$exe_shell" && \
    install -m0755 "$exe_shell" /rootfs/bin/exe-shell && \
    ln -s exe-shell /rootfs/bin/bash && \
    ln -s exe-shell /rootfs/bin/sh && \
    cp /etc/nixos/configuration.nix /rootfs/etc/nixos/configuration.nix && \
    printf '%s\n' \
      'root:x:0:0:System administrator:/root:/bin/exe-shell' \
      'exedev:x:1000:100:exe.dev user:/home/exedev:/bin/exe-shell' \
      > /rootfs/etc/passwd && \
    printf '%s\n' \
      'root:x:0:' \
      'wheel:x:1:exedev' \
      'sshd:x:22:' \
      'users:x:100:exedev' \
      > /rootfs/etc/group && \
    : > /rootfs/etc/machine-id && \
    chown 1000:100 /rootfs/home/exedev

FROM scratch

COPY --from=builder /rootfs/ /

ENV container=oci
ENV PATH=/run/current-system/sw/bin:/sw/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin

WORKDIR /home/exedev

LABEL exe.dev/login-user=exedev

EXPOSE 8000 9999

CMD ["/init"]
