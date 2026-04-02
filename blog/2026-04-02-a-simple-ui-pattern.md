---
title: A Transparent UI Pattern
description: exe.dev's UI shows you the command it's running, so you always know how to script it.
author: Philip Zeyliger
date: 2026-04-02
embargo: "2026-04-02T08:00:00-07:00"
published: true
---

<img src="https://boldsoftware.github.io/public_html/share-button.gif" alt="The exe.dev UI showing the command being run" style="max-width: 100%; height: auto;" />

exe.dev has one API: it's the command you would write in the exe.dev lobby. For
example, `share add island-anchor "philip+demo2@bold.dev"` shares the HTTP
server for the VM island-anchor to that e-mail address. (We believe sharing a
web app should be as simple as sharing a document!) You can do that very same
command over our HTTP API. Our UI tells you what command it's running, right down to
the quoting.

We do this for two reasons:

First, it teaches our users that everything they're doing in the UI, they (and
their agents!) can do in the CLI. We know our users are going to want to script.

Second, it keeps us honest. The UI can't cheat. If the system doesn't expose
the right API knobs, we find out immediately because the UI uses the same
interface.

We like this pattern. We hope you do too.
