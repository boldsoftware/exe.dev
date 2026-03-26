# Building Exeuntu Locally

Build the exeuntu Docker image from the `exeuntu/` directory:

```
cd exeuntu
make
```

This downloads the latest shelley binary and builds `ghcr.io/boldsoftware/exeuntu:latest` locally. Requires Docker with buildx.

## Testing with a Local Registry

To test a locally-built image with exe, push it to a registry the exelet can pull from. One approach using Tailscale for TLS:

```
go install github.com/boinkor-net/tsnsrv/cmd/tsnsrv@latest
docker run -d -p 5555:5000 --restart always --name registry registry:3
tsnsrv -name $USER-registry -suppressWhois=true http://localhost:5555
```

Then tag, push, and use:

```
docker tag ghcr.io/boldsoftware/exeuntu:latest $USER-registry.<tailnet>.ts.net/boldsoftware/exeuntu:latest
docker push $USER-registry.<tailnet>.ts.net/boldsoftware/exeuntu:latest
```

```
localhost > new --image=$USER-registry.<tailnet>.ts.net/boldsoftware/exeuntu:latest
```
