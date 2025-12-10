You can run "make" to build the exeuntu image locally, assuming you have a
working Docker with buildx. (`brew install docker-buildx` may be necessary.)

TODO: Feel free to make the following instructions into a make
target :)

## Running a "local" registry

Docker registries expect HTTPS and you have to go through some hoops
to do the "insecure" setup.

So, to run a local registry, we run a registry in Docker, and we use
`tsnsrv` to create an SSL proxy with Tailscale SSL certs. This assumes
you have a $TS_AUTHKEY set.

```
 go install github.com/boinkor-net/tsnsrv/cmd/tsnsrv@latest
 # On my machine, port 5000 was "AirTunes", so I used 5555.
 docker run -d -p 5555:5000 --restart always --name registry registry:3
 # In some separate window
 tsnsrv -name $USER-registry -suppressWhois=true http://localhost:5555
```

## Pushing/pulling

Replace "philip-registry" as appropriate.

```
    docker tag ghcr.io/boldsoftware/exeuntu:latest philip-registry.crocodile-vector.ts.net/boldsoftware/exeuntu:latest
    docker push philip-registry.crocodile-vector.ts.net/boldsoftware/exeuntu:latest
```

## Using with exe

```
localhost ▶ new --image=philip-registry.crocodile-vector.ts.net/boldsoftware/exeuntu:latest
```

And voila!
