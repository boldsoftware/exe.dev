# NixOS userland for exe.dev

This Dockerfile serves as an example/prototype of using Nix on exe.dev.
It uses https://ttl.sh/ which is a temporary and public Docker registry.

```sh
IMAGE=ttl.sh/exe-nixos-$(uuidgen):24h
docker build -t "$IMAGE" .
docker push "$IMAGE"
ssh exe.dev new --image="$IMAGE"
```
