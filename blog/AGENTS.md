# Blog

Posts are `YYYY-MM-DD-slug.md` with YAML frontmatter (`title`, `description`, `author`, `date`, `published`). The date must match the filename. Supports markdown plus raw HTML.

Dev server (live reload): `go run ./cmd/blogd -http :8080`

Always run `go test ./blog/...` after editing posts. A malformed post breaks `Load()`.

Set `published: true` to publish. Do not push to main without approval.
