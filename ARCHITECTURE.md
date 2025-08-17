Here's a diagram that Claude built from an excalidraw I drew...


User                                exe.dev Service
                                                           
┌─────────────────┐                            ┌─────────────────────────────────┐
│                 │                            │        docker-01 (-02...) (AWS) │
│     SSH         │                            │                                 │
│user.team@exe.dev│                            │  ┌───────────────────────────┐  │
│                 │                            │  │    user.team container    │  │
└─────────────────┘                            │  │        (gVisor)           │  │
         │                                     │  │                           │  │
         │                                     │  │  ┌───────────────────┐    │  │
         │                                     │  │  │  py -mhttp.server │    │  │
         │                                     │  │  └───────────────────┘    │  │
         │                                     │  │                           │  │
         │                                     │  │  ┌───────────────────┐    │  │
         │                                     │  │  │      sshd -i      │    │  │
         │                                     │  │  └───────────────────┘    │  │
         │                                     │  └───────────────────────────┘  │
         │                                     │                                 │
         │       ┌─────────────────────────┐   │  ┌────────────────────────────┐ │
         └───────┤      exed-01 (on AWS)   │   │  │        dockerd             │ │
                 │                         │   │  │   (gvisor backend)         │ │
                 │  ┌─────────────────┐    │   │  └────────────────────────────┘ │
                 │  │      exed       │    │   │                ║                │
                 │  │                 │    │   │     ┌──────────╨──────────┐     │
                 │  │   db.sqlite     │    │   │     │     EBS Disk(s)     │     │
                 │  └─────────────────┘    │   │     │ Containers use XFS  │     │
                 │           │             │   │     │    with Quotas      │     │
                 │           │             │   │     └─────────────────────┘     │
                 │  ┌─────────────────┐    │   └─────────────────────────────────┘
                 │  │     sshpiper    │    │                     ▲
                 │  │                 │    │                     │
                 │  │ ┌─────────────┐ │    │─────────────────────┘
                 │  │ │ SSH Proxy   │ │    │
                 │  │ └─────────────┘ │    │
                 │  └─────────────────┘    │
                 └─────────────────────────┘
                           │
                           ▼
                 ┌─────────────────┐
                 │     Stripe      │
                 └─────────────────┘


exe.dev provides containers/VMs very, very easily over SSH.

## SSH Architecture

Users SSH into exe.dev, which is handled by sshpiper. sshpiper
talks to exed over a gRPC plugin interface to find out what to
do with the connection. Connections for "host.team@exe.dev" with
appropriate credentials are forwarded onto the container itself.
Connections for the "exe.dev shell" are forwarded to exed directly.

sshd is set up on container credential with new credentials stored
in the database.

## HTTP Architecture

For HTTP, connections https://host.team.exe.dev/ are handled by exed directly
and proxied to their containers.

## Container Management

Container creation happens in exed. A random docker_host is chosen, and the
container is created with the gvisor Docker container runtime. Credentials
for the container are generated and stored in the database.
