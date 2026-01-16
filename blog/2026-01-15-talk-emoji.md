---
title: Talk emoji to me
description: Emoji reactions are a surprisingly effective way to communicate with bots.
author: Josh Bleecher Snyder
date: 2026-01-15
tags:
  - exe.dev
published: true
---

Start-ups sprout ad hoc processes constantly. I've noticed an emerging theme among ours at [exe.dev](https://exe.dev/): emoji reactions are a great way to communicate with bots.

They’re efficient: adding an emoji is easy.

They’re low noise: they don’t generate unread messages for anyone or threads to follow.

They’re visible: other people can see that it has been done, and who did it.

They're intuitive: 🪲 to auto-file an issue, 💡 for feature requests. And they take place in the exact context of whatever inspired action.

Our bots also communicate back with emoji reactions. 👀 is a good placeholder for long-running requests. ✅ indicates success; 💥 is failure.

Here’s a concrete example.

We shipped invite codes today. How do we know when someone requests an invite code? It shows up in Slack. If we want to grant that person their code, we emoji respond with the number of invites to give them, e.g. 3️⃣. ✅ appears. Move on. Lightweight, easy, fast, and transparent.

I can hear you saying, "Gee, this sounds like a great idea and all, but deploying a Slack bot is oodles of effort." You will no doubt be shocked to learn that our Slack bot and Discord bot run on [exe.dev](https://exe.dev/). When my colleague wanted to add another emoji handler, he pulled up [Shelley](/shelley) on that bot's VM and prompted his way to an incremental feature. So far, we have a Discord bot, a Slack bot, a link shortener, a query-my-logs agent, and a blog, all hosted on exe.dev, with no sign of slowing down.
