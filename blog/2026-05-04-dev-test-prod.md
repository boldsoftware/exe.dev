---
title: "Dev, Test, Prod: Choose One, Two, or Three"
description: You can build, test, and deploy on exe.dev.
author: Philip Zeyliger
date: 2026-05-04
published: true
embargo: "2026-05-04T09:00:00-07:00"
---

<img src="/assets/devprodtest-venn.svg" alt="Venn diagram showing Dev, Test, and Prod overlapping with exe.dev in the center" width="100%"/>

Industry-wide, we often develop our software in three distinct environments.
Perhaps your laptop is a Mac; your CI system is hosted GitHub Actions, and your
prod is k8s.

# Three-in-One

For some use cases, you need not bother with the complexity; use one exe.dev vm
for all three. A blog, a dashboard, a link shortener, a bot, and so on: these
work well with the environments collapsed. Add features by asking Shelley to do
so. Set up continuous deployment by asking Shelley to poll every hour. Use git
for a backup if it calls for it. Voila!

<div style="position: relative; overflow: hidden; border: 1px solid #e5e7eb; border-radius: 8px; padding: 1rem 6rem 1rem 1rem; margin: 1rem 0;">
  <p style="margin: 0;">Our internal tools sport an "Edit with Shelley" ribbon.
  They either point straight to the "vm.shelley.exe.xyz" domain, or link
  to exe.dev/new with a pre-filled prompt and pre-filled tags, just
  like the link here.</p>
  <a href="https://exe.dev/new?prompt=Clone%20github-thing.int.exe.xyz%20and%20do:...&tags=github-thing" target="_blank" rel="noopener noreferrer" style="position: absolute; top: 30px; right: -32px; background: #fbbf24; color: #78350f; padding: 5px 44px; font-size: 11px; font-weight: 600; transform: rotate(45deg); box-shadow: 0 1px 3px rgba(0,0,0,0.15); text-align: center; letter-spacing: 0.2px; white-space: nowrap; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; text-decoration: none;">Edit with Shelley</a>
</div>

# Just Dev

Use an exe.dev vm (or many) to work on your software. Set up the GitHub
integration ([docs](https://exe.dev/docs/integrations-github)) to make cloning easy. Some people work
serially. Some people work using multiple worktrees on one vm. Some people have
one vm per task or project. Clone your VMs using ‘cp’ or configure them using
setup scripts.

Using remote VMs opens up the convenience of mobile, opportunities for sharing,
not to mention isolation from your other projects. 

<img src="/assets/devprodtest-seesaw.svg" alt="Seesaw balancing customization against convenience" width="100%"/>

Why now? Many, many companies have tried remote development before. There is
an entire graveyard of failed startups in this space. The big difference is
agents. If your development is increasingly chat-based, the old arguments about
getting your environment and dot-rc files just right fade away. The convenience
of starting a task from your phone overwhelms the decades-old bashrc file and
finely crafted PS1. As a bonus, you get the ability to share with your co-workers.
Pull requests are so yesterday; send them a link to a working demo instead.

# Just Test

Exe.dev VMs are a great place to riff on an idea. Perhaps you want to explore a
particular open source project. Or you want to do some data analysis and share
it with your co-workers? Or prototype your next idea? Or find your flakes by
running your tests over and over again. Or let loose Shelley, our agent, on
your app with its built-in browser? Or send off a security review. Or even
just run a GitHub Actions runner.

Because you pick what access you want to give your VMs, and because they’re
[persistent](https://exe.dev/docs/serverful), exe.dev VMs are great
places to test stuff out. 

# Just Prod

You can host real, production software in exe. We support custom domains with a bit of DNS
configuration ([docs](https://exe.dev/docs/cnames)).

If you’re incredulous that this is a good idea, the entirety of Stack Overflow
ran on [just a few
machines](https://nickcraver.com/blog/2016/02/17/stack-overflow-the-architecture-2016-edition/).
Reach out to us if you want to enlarge your VM as far as modern hardware can
go.

# Private, Internal, or Public

Once you build it, you'll want to share it. You can keep it to yourself,
and that's the default. Or you can share it with your team or with share links.
Or you can share it publically. Sharing a VM's website is as easy as sharing
any other online doc.
