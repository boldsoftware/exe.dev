#!/bin/sh
IFACE=tailscale0

# allow loopback and tailscale; drop everything else
sudo iptables -A INPUT -i lo -j ACCEPT
sudo iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo iptables -A INPUT -i "$IFACE" -j ACCEPT
sudo iptables -A INPUT -j DROP

# outbound stays allowed
sudo iptables -P OUTPUT ACCEPT
