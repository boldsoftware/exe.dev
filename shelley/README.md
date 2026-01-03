# Shelley: a coding agent for exe.dev

Shelley is a mobile-friendly, web-based, multi-conversation, multi-modal,
multi-model, single-user coding agent built for but not exclusive to
[exe.dev](https://exe.dev/). It does not come with authorization or sandboxing:
bring your own. 

*Mobile-friendly* because ideas can come any time.

*Web-based*, because terminal-based scroll back is punishment for shoplifting in some countries.

*Multi-modal* because screenshots, charts, and graphs are necessary, not to mention delightful. 

*Multi-model* to benefit from all the innovation going on.

*Single-user* because it makes sense to bring the agent to the compute.

# Architecture 

The technical stack is Go for the backend, SQLite for storage, and Typescript
and React for the UI. 

The data model is that Conversations have Messages, which might be from the
user, the model, the tools, or the harness. All of that is stored in the
database, and we use a SSE endpoint to keep the UI updated. 

# History

Shelley is partially based on our previous coding agent effort, [Sketch](https://github.com/boldsoftware/sketch). 

Unsurprisingly, much of Shelley is written by Shelley, Sketch, Claude Code, and Codex. 

# Open source

Shelley is Apache licensed. We require a CLA for contributions.

# Building Shelley

Run `make`. Run `make serve` to start Shelley locally.

## Dev Tricks

If you want to see how mobile looks, and you're on your home
network where you've got mDNS working fine, you can
run 

```
socat TCP-LISTEN:9001,fork TCP:localhost:9000
```
