# exe-ops

exe-ops is an application that is designed to have a single pane of visibility into the exe infrastructure. It's meant to provide the following:

- basic system info (cpu, mem, disk, network)
- system upgrades
- exelet / exeprox status
- zfs snapshot replication status, zpool availability
- server tags

It consists of a very lightweight Go based agent that collects system info and forwards to a remote exe-ops server. The exe-ops server receives and aggregates the last 7 days worth of info. Data is stored in SQLite using modernc.org pure Go library.

# Agent

Binary: `exe-ops-agent`

The exe-ops agent is responsible for gather the following basic metrics:

- cpu (current percentage in use)
- memory (free, used, swap)
- disk (free, used)
- network (send/recv)
- region (deduced from the hostname in the form of <exelet|exeprox>-<region>-<environment>-<instance> - for example, exelet-nyc-prod-01).
- exe component info
   - exelet (if present) version and status
   - exeprox (if present) version and status

Agents use token based authentication and support specifying a name when connecting to the server.

# Server

Binary: `exe-ops-server`

The exe-ops server serves via HTTP and provides the user interface for management as well as the endpoints to receive agent data over HTTP. If agent payloads are large, use HTTP streaming. The server uses simple token based authentication that is configured in the headers and sent from the agent.

## User Interface

The user interface for the server is a simple and clean Vue3 based UI using PrimeVue components and PrimeIcons and dark mode support. The user interface is compiled and served directly from the Go binary for simple deployment. The following views should be supported:

- Dashboard
   - Server overview info
      - Name, location, snapshot of CPU, Mem, Disk, Network
- Server Details
   - Name
   - Location / Region
   - Uptime
   - Components (exe components)
   - System Updates available
   - zfs metrics
      - tank usage (used, free)
   - Tags

