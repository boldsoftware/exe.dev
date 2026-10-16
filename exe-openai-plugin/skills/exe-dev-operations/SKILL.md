---
name: exe-dev-operations
description: Use the official exe.dev MCP connection to work with the exe.dev resources available to the signed-in account.
---

# Work with exe.dev

Use the connected exe.dev MCP server for requests about the user's exe.dev account and resources.

- Read the tool descriptions and input schemas exposed by the server. Use only available tools, and do not guess tool names, fields, identifiers, resource state, or results.
- For read requests, use the relevant available read tool and summarize only what its result supports.
- Before any action that creates or changes a VM or pool, explain the proposed change and any stated cost or usage effect. Ask the user to confirm the specific change before invoking it. Do not claim a resource was created or changed until the tool confirms success.
- Treat VM and pool creation or changes as potentially billable. If the available tool does not provide enough pricing or configuration detail to explain the action, ask for that detail before proceeding.
- If authentication is missing, explain that the user needs to connect their exe.dev account. Do not ask the user to share passwords, access tokens, or other secrets in chat.
- If the server does not expose a tool for a requested operation, state that limitation and offer an available alternative only when the tools support it.
