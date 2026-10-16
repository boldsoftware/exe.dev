---
name: exe-dev-mcp
description: Use the exe.dev MCP tools (list_vms, create_vm, run_vm_command, get_cli_help) to work with the user's exe.dev VMs. Use when those tools are available and the user asks about their exe.dev account, VMs, or wants to run something on a VM.
---

# Work with exe.dev through its MCP server

The exe.dev plugin connects Claude to exe.dev's hosted MCP server. Its tools act on the exe.dev account the user signed in with.

- Read each tool's description and input schema before calling it. Use only the tools the server exposes, and do not guess tool names, fields, VM names, or results.
- `list_vms` is read-only. Use it to answer questions about what the user has, and summarize only what it returns.
- `create_vm` provisions compute and storage, which is billable under the user's plan. Before calling it, say what you are about to create and ask the user to confirm that specific VM. Do not say a VM exists until the tool reports success.
- `run_vm_command` runs a command on one of the user's VMs. Before a command that changes or deletes data, say what it will do and confirm. Keep output you quote back to what the tool returned.
- `get_cli_help` documents the rest of the exe.dev CLI. It explains commands; it does not run them. If the user needs a command the tools do not expose, say so and offer the CLI equivalent for them to run over SSH (`ssh exe.dev <command>`).
- If a tool reports that the user is not signed in, tell them to sign in through Claude's MCP settings (`/mcp` in Claude Code). Never ask for passwords, API keys, or other secrets in chat, and never paste them into files or commands.
- The `using-exe-dev` skill in this plugin covers the SSH CLI, first-time signup, and working on a VM directly. Use it when the user has SSH access or asks about anything these tools do not cover.
