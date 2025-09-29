# Welcome Server

This example web server demonstrates end-to-end usage of exe.dev:

- Login/logout behind the exed proxy using its auth URLs
- Show who is logged in (when proxy headers are present)
- Track per-user page views in SQLite

After making changes: `make build` and then `sudo make restart`.

Auth flow

- When not logged in, the page shows a Login link to:
  `https://{main-domain}/auth?redirect={path}&return_host={this-host}`
  This sends the user to the main domain to authenticate, then returns them to
  `https://{this-host}` and sets the subdomain cookie.
- When logged in, the page shows a Logout link to:
  `https://{this-host}/__exe.dev/logout`

Identity headers

- When proxied through exed, requests will include `X-ExeDev-UserID` and `X-ExeDev-Email` if the user is authenticated via exe.dev.
  If present, the welcome server shows who you are and counts your visits.

SQLite storage

- The server stores per-visitor counts in SQLite.
- The server manages migrations, auto-creates a `visitors` table,
  and upserts counts per request.

Code layout

- `cmd/welcomed`: main package (binary entrypoint)
- `srv`: HTTP server logic (handlers)
- `db`: SQLite open + migrations (001-base.sql)
- `sqlc`: optional sqlc schema/queries for codegen
