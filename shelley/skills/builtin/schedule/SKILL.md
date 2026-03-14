---
name: schedule
description: Use when a user requests a task to be done later or on a schedule.
---

Use systemd user timer units. Unless the user explicitly asked to schedule something, have the user confirm.

If the task is obvious and straightforward, such as a bash command, you might schedule just the command. Otherwise, schedule a future Shelley conversation by calling `shelley client chat`.

Name shelley-calling units `shelley-<name>.{service,timer}`. The service ExecStart should invoke:

```
shelley client chat -p '<prompt>' -cwd '<working_directory>'
```

Each timer firing always creates a new conversation so that no conversation grows without bound.

The prompt baked into the service unit should concisely convey the overarching goals and context from the user, preferably in their own words, as well as the specific task being achieved by this scheduled invocation. The prompt must always include the originating conversation ID (from `$SHELLEY_CONVERSATION_ID`), so that new agent can refer to the originating conversation for additional context if needed.

You are responsible for ensuring that one-shot units will be cleaned up.
