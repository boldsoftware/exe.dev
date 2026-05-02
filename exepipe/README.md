# Exepipe

The exepipe program is a small server that copies data between network
descriptors and listens on network sockets and forwards any
connections to other servers.
Using a small, simple, program for data copying means that we can
update, install and restart more complex programs like exeprox or
exelet without disturbing existing connections.
For example, existing user SSH connections to user VMs will keep
running uninterrupted even if exeprox restarts.

## Exepipe server

The exepipe server listens for commands on a Unix socket, defaulting
to the abstract Unix socket address `@exepipe`.

## Exepipe client

Other programs contact the exepipe server via the API defined in the
exe.dev/exepipe/client package.

## Exepipe operations

There are two main exepipe operations.

### Copy

The copy operation takes two file descriptors.
Exepipe will copy data between the two descriptors,
continuing to do so until the descriptors are closed.
These copies will normally be done using the `splice` system call,
requiring minimal work on the part of the exepipe process.

A typical use is when an HTTPS request is made to a VM.
The request will arrive at an exeprox server,
which will do TLS termination and verify that the request meets all
access requirements for the VM.
The exeprox server will then open an SSH connection to the VM,
typically contacting a web server running on the VM.
At that point the exeprox server will have a network connection that
arrived from some external source, and a network connection to a
server running on a VM.
The exeprox server will pass both those descriptors as part of a copy
command to the exepipe server.
The exepipe server will take over transferring data between those two
descriptors, thus supporting an arbitrary complex connection between
the external client and the server running on the VM.

### Listen

The listen operation takes one file descriptor,
and a host, port, and an optional network namespace.
The file descriptor is a `net.Listener`.
Exepipe will listen for incoming connections on the descriptor.
For each new connection, exepipe will open a connection to the
specified host and port in the specified network namespace.
If the host is all digits, it is considered to be a vsock CID,
and exepipe will open a vsock connection to the port in that CID;
in this case specifying the network namespace is an error.
Exepipe will then copy data between the two connections,
as for the copy operation.

A typical use is exposing the SSH port for a user VM.
Each user VM currently has a unique TCP address and port.
Connecting to that address and port connects to the SSH server running
on the VM.
This is implemented by having a listener running on an exelet machine,
listening on some external visible port.
When any connection is made to that port,
it is forwarded to the SSH port on the VM,
a TCP address that is only available on the local machine.
The exelet daemon manages these ports for all the local VMs by
opening listeners on the appropriate ports on the Tailscale address,
and directing exepipe to listen for and handle incoming connections.

(Note: one can imagine a different approach in which exeprox contacts
exelet on a single known port and specifies the VM.
Exelet will open a connection to the SSH port on that VM.
Exeprox will then copy the SSH connection to exelet,
and exelet will copy the connection to the VM.
This is more complex in that it's not a pure SSH connection,
but it's simpler in that we don't need a separate listening socket for
each VM.)

#### Unlisten

The unlisten operation turns off an existing listener, used when a VM
is destroyed or migrated.

#### Listeners

The listeners operation asks exepipe for the current list of
listeners.
This is used by the exelet daemon when it starts up,
to make sure that the set of listeners is as desired,
without disturbing any listeners that are already in the correct
state.

## Exepipe updates

As exepipe is fairly simple, we do not expect to have to update it
very often.
That said, there may be cases where updates are required.
The exepipe server is designed to support seamless updates with
minimal disruption to existing connections.

The exepipe server, like other exe servers, is controlled by systemd.
Systemd will start the exepipe servers with a `-controller` option.
Exepipe with that option will start the real exepipe as a child
process.
The main process will do nothing.
When systemd is asked to restart exepipe, it will kill the main
process but leave the child process running.
The new exepipe will again be run with the `-controller` option.
It will again start a new child process.

The new child process will see that there is an existing exepipe
listening on the Unix socket.
It will contact the existing exepipe.
The old exepipe will pass the server socket to the new exepipe.
The old exepipe will pass all existing listeners to the new exepipe.
All of these operations mean that the new exepipe will be handling all
new incoming connections in place of the old exepipe.

The old exepipe will continue managing all copy operations.
Since those copy operations are sitting in a `splice` system call,
there is no way to cleanly interrupt the connections and transfer them
to the new exepipe.
Therefore, the old exepipe will continue handling existing copy
operations, but will not start any new ones.
When all existing copy operations are closed,
the old exepipe will exit.
