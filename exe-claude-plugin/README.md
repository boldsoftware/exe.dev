# exe.dev plugin for Claude

The official [exe.dev](https://exe.dev) plugin for Claude. It connects Claude to exe.dev's hosted MCP server, so Claude can list your VMs, create new ones, and run commands on them, and it teaches Claude the exe.dev CLI so it can work on your VMs over SSH.

exe.dev gives you Linux VMs with persistent disks, instant HTTPS, and built-in auth, managed entirely over SSH.

## Install

Once the plugin is listed in Anthropic's directory, add it from the directory in claude.ai or Claude Code.

From Claude Code, you can also install it straight from this repository:

```
claude plugin install exe-dev --marketplace boldsoftware/exe.dev
```

Then sign in: open `/mcp` in Claude Code, pick **exe-dev**, and your browser opens to connect your exe.dev account. Ask Claude to "list my exe.dev VMs" to check it worked.

## What the plugin contains

- **One MCP server**, `exe-dev`, at `https://exe.dev/mcp/claude`. It speaks the Model Context Protocol over Streamable HTTP and signs you in with OAuth in your browser. It exposes four tools: `get_cli_help`, `list_vms`, `create_vm`, and `run_vm_command`. Creating a VM is billed through your exe.dev account under your plan.
- **Two skills.** `exe-dev-mcp` tells Claude how to use those tools carefully, including asking before anything billable or destructive. `using-exe-dev` is exe.dev's [agent skill](https://exe.dev/docs/agent-skill): the CLI quick reference, first-time signup, and how to work on a VM directly over SSH.

Nothing runs on your machine. The plugin has no hooks, scripts, local commands, or bundled code. The only network traffic it causes goes to exe.dev, and only through the MCP server above. What a tool can do is bounded by the exe.dev account you sign in with.

Developers who want the full exe.dev CLI as a single `exec` tool can add the general endpoint instead, as [the MCP docs](https://exe.dev/docs/mcp) describe:

```
claude mcp add --transport http exe https://exe.dev/mcp
```

## Develop and test

From the folder that contains this one:

```
claude plugin validate ./exe-claude-plugin --strict
claude --plugin-dir ./exe-claude-plugin
```

The `using-exe-dev` skill is a copy of [`skill/SKILL.md`](https://github.com/boldsoftware/exe.dev/blob/main/skill/SKILL.md) in this repository, so people who use the plugin and people who install the skill on its own read the same guidance. Change the original and copy it here; a check keeps the two identical.

## Releases

Raise `version` in `.claude-plugin/plugin.json` with every change you want users to receive. Anthropic's directory follows the `main` branch of this repository and publishes each new version after its checks pass.

## Links

- [exe.dev docs](https://exe.dev/docs) and the [MCP server page](https://exe.dev/docs/mcp)
- [Getting help](https://exe.dev/docs/getting-help)
- [Privacy notice](https://exe.dev/docs/privacy-notice) and [terms of service](https://exe.dev/docs/terms-of-service)
