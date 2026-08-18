{ config, lib, pkgs, modulesPath, ... }:

let
  exeShell = pkgs.writeShellScriptBin "exe-shell" ''
    if [ -x /run/current-system/sw/bin/bash ]; then
      shell=/run/current-system/sw/bin/bash
    else
      shell=/sw/bin/bash
    fi

    export PATH="$HOME/.nix-profile/bin:/run/current-system/sw/bin:/sw/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    exec "$shell" "$@"
  '';
in
{
  imports = [ "${modulesPath}/profiles/docker-container.nix" ];

  system.stateVersion = "26.05";
  installer.cloneConfig = false;

  # exe-init configures the NIC, routes, DNS, hostname, and hosts file before
  # handing PID 1 to NixOS. Do not race it with DHCP or another sshd.
  networking.hostName = "";
  networking.useDHCP = false;
  networking.useHostResolvConf = false;
  networking.resolvconf.enable = false;
  environment.etc.hosts.enable = false;
  networking.firewall.enable = false;
  services.openssh.enable = false;

  users.mutableUsers = false;
  # exe.dev injects and serves SSH public keys outside the NixOS OpenSSH
  # configuration, so NixOS cannot see the login method at evaluation time.
  users.allowNoPasswordLogin = true;
  users.users.root.shell = "/bin/exe-shell";
  users.users.exedev = {
    isNormalUser = true;
    uid = 1000;
    group = "users";
    extraGroups = [ "wheel" ];
    home = "/home/exedev";
    shell = "/bin/exe-shell";
  };
  # exe-init's embedded OpenSSH daemon requires this privilege-separation
  # account to remain present after NixOS activation rewrites /etc/passwd.
  users.groups.sshd = { };
  users.users.sshd = {
    isSystemUser = true;
    group = "sshd";
    home = "/var/empty";
  };
  security.sudo.wheelNeedsPassword = false;

  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  environment.systemPackages = [ exeShell ] ++ (with pkgs; [
    bashInteractive
    cacert
    curl
    file
    git
    jq
    less
    neovim
    procps
    psmisc
    ripgrep
    rsync
    sqlite
    tree
    unzip
    wget
  ]);

  # exe.dev's ssh server intentionally supplies a conventional PATH. A tiny
  # login-shell wrapper adds the NixOS system profile for both interactive and
  # command-mode SSH, and is recreated by every NixOS activation.
  system.activationScripts.exeDevShell = lib.stringAfter [ "users" ] ''
    install -Dm0755 ${exeShell}/bin/exe-shell /bin/exe-shell
    ln -sfn exe-shell /bin/bash
    ln -sfn exe-shell /bin/sh
  '';
}
