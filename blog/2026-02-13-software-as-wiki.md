---
title: Software as Wiki, Mutable Software
description: When your coding agent lives next to your software, editing it is as easy as editing a wiki.
author: Philip Zeyliger
date: 2026-02-13
tags:
  - shelley
published: true
---

<div style="display: flex; align-items: center; justify-content: center; gap: 12px; margin: 2em 0; flex-wrap: wrap;">
  <div style="text-align: center; flex: 1; min-width: 200px;">
    <img src="https://boldsoftware.github.io/public_html/slinky/slinky.png" alt="Slinky link shortener" style="max-width: 100%; border-radius: 8px;" />
    <div style="width: 180px; height: 70px; margin: 8px auto 0; border: 2px solid #e8a735; border-radius: 6px; overflow: hidden;">
      <img src="https://boldsoftware.github.io/public_html/slinky/slinky.png" style="display: block; width: 600px; max-width: none; margin-left: -453px; margin-top: -70px;" />
    </div>
    <div style="font-size: 12px; color: rgba(0,0,0,0.5); margin-top: 4px;">"Edit with Shelley"</div>
  </div>
  <div style="font-size: 32px; flex-shrink: 0;">&#8644;</div>
  <div style="text-align: center; flex: 1; min-width: 200px;">
    <img src="https://boldsoftware.github.io/public_html/slinky/shelley.png" alt="Shelley editing Slinky" style="max-width: 100%; border-radius: 8px;" />
    <div style="width: 180px; height: 70px; margin: 8px auto 0; border: 2px solid #4a7dff; border-radius: 6px; overflow: hidden;">
      <img src="https://boldsoftware.github.io/public_html/slinky/shelley.png" style="display: block; width: 600px; max-width: none; margin-left: -420px; margin-top: -184px;" />
    </div>
    <div style="font-size: 12px; color: rgba(0,0,0,0.5); margin-top: 4px;">"slinky.exe.xyz"</div>
  </div>
</div>

Here at the exe.dev offices, we built a link shortener. It's called slinky. It's nothing special.

Link shorteners are a dime a dozen. They help in a time of useless URLs (hi,
Google Docs), hard to remember port numbers, and overly clever naming schemes.

The thing that makes this link shortener unusual, is that "Edit with Shelley"
button. When I wanted to add a feature (%s placeholders in short links for
Honeycomb queries for a trace id, say), I click on that, and I'm in the Shelley
agent, on the same VM. I said, and I quote:

> Some slinky URLs have "template" parameters. For example, I want
> http://slinky.exe.xyz/trace/foo to become
> https://ui.honeycomb.io/[%20 %20 so much quoting %20]foo[...]
> Note how "foo" has to be replaced in that mess of escaping. Create a way to put
> a placeholder in the link, and reference it like I mention. While you're add [sic]
> it, add a link for this one.

And then a few minutes later, Shelley had one-shotted this small feature to Slinky.

You can treat some software like a wiki. You don't like it? Click "edit" and change it.
