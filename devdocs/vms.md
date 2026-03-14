# VMs

Inventory of VMs running shared exe.dev stuff on exe.dev. All use the `boldsoftware/exeuntu` image.

## Active Services

| VM | Purpose | Runtime | Notes |
|---|---|---|---|
| `exe-news` | Daily commit briefs + panopticon newsletter | Python (`uv run`) | `daily-brief.service`, `panopticon-newsletter.service` |
| `exe-slack-hud` | Slack HUD bot | Go | Also runs Tailscale |
| `exe-discord-bug` | Files GitHub issues from Discord reactions | Bun/TypeScript | Source in `~/src/discord-bug-bot` |
| `friends` | exe.dev invite code server | Go | |
| `slinky` | URL shortener | Go | |
| `prodlock` | Deployment lock service | Go | Posts to Slack #ship (prod) and #boat (staging) |
| `exe-agents` | Agent state tracking (continuous-codereview, watch-ci-flake) | | Has exe repo cloned |
