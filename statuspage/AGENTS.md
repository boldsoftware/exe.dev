# Agents

## Deploy

The statuspage is deployed on Fly.io as the `exe-status` app.

To deploy, get Fly.io credentials from 1Password (search "Fly" or "fly.io"), then run:

```
fly auth login
fly deploy
```

The app is a single Go binary with embedded templates and CSS.
SQLite database is stored on a persistent Fly volume at `/data/status.db`.
There is no CI/CD pipeline; deploys are manual from this directory.
