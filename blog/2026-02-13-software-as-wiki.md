---
title: Software as Wiki, Mutable Software
description: When your coding agent lives next to your software, editing it is as easy as editing a wiki.
author: Philip Zeyliger
date: 2026-02-13
tags:
  - shelley
published: true
---

<style>
.wiki-hero {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  margin: 2em 0;
}
.wiki-hero-panel {
  text-align: center;
  flex: 1;
  min-width: 200px;
}
.wiki-hero-panel img.screenshot {
  max-width: 100%;
  border-radius: 8px;
}
.wiki-hero-arrow {
  font-size: 32px;
  flex-shrink: 0;
}
.wiki-hero-inset {
  width: 180px;
  height: 70px;
  margin: 8px auto 0;
  border-radius: 6px;
  overflow: hidden;
  position: relative;
}
.wiki-hero-inset img {
  display: block;
  width: 600px;
  max-width: none;
  position: absolute;
}
.wiki-hero-inset-slinky {
  border: 2px solid #e8a735;
}
.wiki-hero-inset-slinky img {
  left: -453px;
  top: -70px;
}
.wiki-hero-inset-shelley {
  border: 2px solid #4a7dff;
}
.wiki-hero-inset-shelley img {
  left: -420px;
  top: -184px;
}
.wiki-hero-caption {
  font-size: 12px;
  color: rgba(0,0,0,0.5);
  margin-top: 4px;
}
@media (max-width: 600px) {
  .wiki-hero {
    flex-direction: column;
    gap: 24px;
  }
  .wiki-hero-panel {
    min-width: 0;
    width: 100%;
  }
  .wiki-hero-arrow {
    transform: rotate(90deg);
  }
  .wiki-hero-inset {
    width: 240px;
    height: 94px;
  }
  .wiki-hero-inset img {
    width: 800px;
  }
  .wiki-hero-inset-slinky img {
    left: -604px;
    top: -93px;
  }
  .wiki-hero-inset-shelley img {
    left: -560px;
    top: -245px;
  }
}
</style>

<div class="wiki-hero">
  <div class="wiki-hero-panel">
    <img class="screenshot" src="https://boldsoftware.github.io/public_html/slinky/slinky.png" alt="Slinky link shortener" />
    <div class="wiki-hero-inset wiki-hero-inset-slinky">
      <img src="https://boldsoftware.github.io/public_html/slinky/slinky.png" alt="" />
    </div>
    <div class="wiki-hero-caption">"Edit with Shelley"</div>
  </div>
  <div class="wiki-hero-arrow">&#8644;</div>
  <div class="wiki-hero-panel">
    <img class="screenshot" src="https://boldsoftware.github.io/public_html/slinky/shelley.png" alt="Shelley editing Slinky" />
    <div class="wiki-hero-inset wiki-hero-inset-shelley">
      <img src="https://boldsoftware.github.io/public_html/slinky/shelley.png" alt="" />
    </div>
    <div class="wiki-hero-caption">"slinky.exe.xyz"</div>
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
