# Go Shelley Template

This is a starter template for building Go web applications on exe.dev. It demonstrates end-to-end usage including HTTP handlers, authentication, database integration, and deployment.

Use this as a foundation to build your own service.

## Building and Running

Build with `make build`, then run `./srv`. The server listens on port 8000 by default.

## Running as a systemd service

To run the server as a systemd service:

```bash
# Install the service file
sudo cp srv.service /etc/systemd/system/srv.service

# Reload systemd and enable the service
sudo systemctl daemon-reload
sudo systemctl enable srv.service

# Start the service
sudo systemctl start srv

# Check status
systemctl status srv

# View logs
journalctl -u srv -f
```

To restart after code changes:

```bash
make build
sudo systemctl restart srv
```

## Authorization

exe.dev provides authorization headers and login/logout links
that this template uses.

When proxied through exed, requests will include `X-ExeDev-UserID` and
`X-ExeDev-Email` if the user is authenticated via exe.dev.

## Database

This template uses sqlite (`db.sqlite3`). SQL queries are managed with sqlc.

## Code layout

- `cmd/srv`: main package (binary entrypoint)
- `srv`: HTTP server logic (handlers)
- `srv/templates`: Go HTML templates
- `db`: SQLite open + migrations (001-base.sql)
