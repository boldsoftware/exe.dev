---
title: Sharing a web site should be as simple as sharing a document
description: use exe.dev boxes as a sandbox
subheading: "7. Blog Posts"
published: false 
---

When we write a document or a spreadsheet, we're used to affordances like
"share via link" and pasting in our co-worker's e-mail address to share with
them. We mark the documents public or not, as we may need.

Sharing a web site is a whole different matter. Not only do you typically need
to work through a labyrinth of settings, you also need to implement
authentication and authorization appropriately for your use case.

It does not have to be so, and exe.dev gives you the typical web-based document
mode of sharing for whatever web site you've figured out. When you run a web
server on your exe.dev box, exe.dev's HTTPS proxy, by default, lets you access
it at https://<box-name>.exe.dev/. You can then use the "share" command in the
exe.dev shell to create share links and share explicitly with certain e-mail
addresses. (And, of course, you can mark your site public.)

This feature lets you focus on the guts of whatever you're doing: your demo,
your app, your site, your *thing*.
