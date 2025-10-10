This directory contains CA certs for use with Pebble and exe.

The certs were generated with:

    ./generate.sh

To avoid browser warnings, you can import `cert.pem` into your operating
system's or browser trust store.

On macOS, you can run:

    sudo security add-trusted-cert -d -r trustRoot \
        -k /Library/Keychains/System.keychain cert.pem

> TODO: Add instructions for other OSes and browsers here.
