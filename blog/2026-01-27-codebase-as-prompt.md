---
title: Your codebase is the prompt
description: When Claude writes code you don't like, ask what it saw, not what it did.
author: Josh Bleecher Snyder
date: 2026-01-27
tags:
  - ai
published: true
---

When Claude writes code I don't like, I used to mutter: "Oh Claude, what did you do?" I now ask instead: "Claude, what did you see?" And then I go address *that*.

Here's a concrete example.

At [exe.dev](https://exe.dev), we have a very substantial end-to-end test suite. It is deeply empiricist: It only performs actions a user (including an admin) can perform, and it only observes state that a user (including an admin) can observe. This works very well with coding agents.

Recently, Claude started writing end-to-end tests that reach into the database. At first, I told Claude not to do that, and it fixed it. Then I got smart. I asked Claude what tests it had modeled its new code on. Sure enough, they were also directly executing queries! Claude wasn't being sloppy; it was being consistent. I fixed the old tests and shut down the loophole in the test infrastructure.

My typical prompt to a coding agent is a few sentences. Maybe a couple of paragraphs, if I'm doing something really involved. The LLM then goes and reads thousands of lines or more from our codebase. Prompts come and go; your code endures.

Agents mirror local style. Your codebase is the prompt. If you're using a state-of-the-art agent, and you don't like the code it generates, don't correct the agent. Instead, improve the code it learned from.

*Cross-posted at [commaok.xyz/ai/codebase-as-prompt/](https://commaok.xyz/ai/codebase-as-prompt/)*
