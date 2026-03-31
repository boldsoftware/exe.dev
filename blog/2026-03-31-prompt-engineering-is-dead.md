---
title: Prompt engineering is dead, but Claude still tries
description: Agents have good judgment now. Delegation beats micromanagement.
author: Josh Bleecher Snyder
date: 2026-03-31
embargo: "2026-03-31T12:00:00-07:00"
published: true
---

A year ago, Claude was better at prompting than I was. Not any more.

Coding agents have gotten dramatically better. Good prompting used to require carefully calibrated instructions and harnesses. Now a good prompt includes goals, context, and maybe some preferences and operational details.

LLMs used to be lackadaisical about following rules. No longer. If you tell them exactly what to do, they will do exactly that. That can be helpful! But in the real, messy world, it's extraordinarily difficult to define in advance a good set of rules. Instead, we constantly exercise judgment. Agents are really good at on-the-fly judgment now. Delegation beats micromanagement.

Most system prompts should be deleted. Most skills should be deleted. Most AGENTS.md should be deleted. It's all getting in the way now; the [bitter lesson](http://www.incompleteideas.net/IncIdeas/BitterLesson.html) has come for harnesses.

My personal CLAUDE.md is 3 lines long. Here it is:

- Do not git push, ever, under any circumstances.
- Do not hand-edit Go imports. Run `goimports -w` after every edit.
- When writing prompts for other agents, convey intent, nuance, and operational details rather than prescriptive instructions—goals are durable, orders are brittle. Trust and delegate over command and control.

I look forward to deleting the goimports line in the near future.

I'd also love to nix the last line, which unfortunately doesn't even work completely. Claude doesn't understand yet that we don't live in 2025.

When Claude barks orders like a drill sergeant, it erases the underlying purpose. Every layer of subagents loses ever more fidelity, like a game of LLM telephone.

The thing is: agents prompt agents all the time. Agents help people write skills. Agents invoke subagents. Agents write scripts that run agents.

Shelley, the exe.dev coding agent, has an orchestrator mode. It works around this form of context collapse by giving all subagents access to a SQLite database containing the entire set of all conversations. Subagents refer back to the user's input as a primary source.
