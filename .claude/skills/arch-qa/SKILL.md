---
name: arch-qa
description: Ask archbot questions about how the exe.dev codebase fits together. Use when you need to understand how systems, packages, or components interact.
user-invocable: false
---

When you need to understand how parts of the exe.dev codebase connect, use the `archbot` agent to ask architecture questions.

To use this skill, spawn archbot as a subagent with your question:

```
Agent(subagent_type="archbot", prompt="<your architecture question>")
```

Examples of good questions:
- "How does a Shelley LLM request flow from the VM to the provider and back?"
- "What is the relationship between execore and exeprox?"
- "How does the billing system connect to the LLM gateway credit system?"
- "What components are involved when a user runs `ssh exe.dev new`?"

archbot will read the codebase (starting from ARCHITECTURE.md) and give you a grounded answer with file/line references. It does not write code.
