#!/bin/sh
OUTIF=$(ip route | grep default | awk '{print $5}')
echo "Will apply network limits to interface: $OUTIF"
read -p "Continue? [y/N] " confirm
if [ "$confirm" != "y" ] && [ "$confirm" != "Y" ]; then
    echo "Aborted."
    exit 1
fi
tc qdisc add dev $OUTIF root handle 1: htb default 30
tc class add dev $OUTIF parent 1: classid 1:1 htb rate 10gbit
tc class add dev $OUTIF parent 1:1 classid 1:10 htb rate 100mbit ceil 100mbit
tc filter add dev $OUTIF parent 1: protocol ip prio 1 u32 match ip src 10.42.0.0/16 flowid 1:10
