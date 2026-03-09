#!/bin/sh
IFACE=tailscale0

# Flush existing rules so this script is idempotent (safe to re-run on boot)
iptables -F INPUT

# allow loopback and tailscale; drop everything else
iptables -A INPUT -i lo -j ACCEPT
iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A INPUT -i "$IFACE" -j ACCEPT
iptables -A INPUT -p tcp --dport 22 -j ACCEPT
iptables -A INPUT -j DROP

# outbound stays allowed
iptables -P OUTPUT ACCEPT
