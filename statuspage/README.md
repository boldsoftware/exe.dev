# statuspage

Status page for exe.dev, deployed at https://status.exe.dev.

Single Go binary backed by SQLite. Runs on Fly.io.

## Deploy

Get Fly.io credentials from 1Password (search "Fly" or "fly.io"), then:

```
fly auth login
fly deploy
```

This builds the Docker image on Fly's remote builders and deploys it.
The app name (`exe-status`) and region (`sjc`) are configured in `fly.toml`.

Data lives on a persistent Fly volume mounted at `/data`.
