---
title: Edit in production
description: A keyboard bug I'd normally ignore took moments to fix in production.
author: Blake Mizerany
date: 2026-02-14
tags:
  - shelley
published: false
---

Philip wrote about [Software as Wiki](https://blog.exe.dev/software-as-wiki) this morning. Its screenshots went stale just before the post shipped.

This is why.

This happened because I got that papercut you get when a little thing like `Tab` doesn't work. Josh and Philip built [Slinky](https://slinky.exe.xyz), an internal link shortener. Type a few letters, narrow the list, grab the link. It worked. Except `Tab` landed on the wrong element. I'd focus the link I wanted and end up on the copy button in the first row.

Every engineer has a list of these papercuts. The tab order that's slightly off. The search that doesn't clear on escape. You think "if it just worked..." and move on, because the cost of fixing it — clone, set up, match versions, get permissions — dwarfs the annoyance. So you flinch through it again tomorrow.

Slinky had an "Edit with Shelley" button. I clicked it.

<img src="https://boldsoftware.github.io/public_html/slinky/slinky.png" alt="Slinky before the updates" style="max-width: 100%; height: auto;" />

That button drops you into the same [exe.dev](https://exe.dev) VM that runs the app. Not an ephemeral container. A real machine with the project's dependencies, with `sudo`, with state that persists. I told Shelley to fix the tab order. It made each result row a native `<a>`, deleted the keydown handler that had been faking link behavior, and `Tab` worked. Moments. No setup. No yak shave.

Because the friction was gone, I kept going. Cleaner search. Better bookmarklet flow. Near-duplicate detection. Small things, each one a prompt to Shelley. None of it was on my list that morning.

After my session with Shelly, I wanted to write this post.

I figured I'd need an "after" screenshot, but Slinky is full of internal links.
I was not about to spin up another version with fake data. But Shelley was
already in front of me and had access to the live service and all the tools it
needed to do clever things that normally would take me hours.

<img src="/assets/slinky-workflow-strip.png" alt="Shelley's summary of the changes" style="width: 100%; height: auto;" />

That's right. Shelly did that. One shot.

My touches went live immediately.

<img src="/assets/slinky-live-redacted.png" alt="Redacted screenshot of the live Slinky service" style="max-width: 100%; height: auto;" />
