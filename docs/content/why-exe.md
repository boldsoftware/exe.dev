---
title: Why EXE?
description: EXE is just a computer.
subheading: "1. Introduction"
suborder: 1
published: true
---

Developers need computers. Sometimes we need those computers to be on the internet, to keep running when we close our laptop lid or when our desktop goes to sleep. We need that because they have work to do in a cron job, or our colleagues or friends need access to them.

These computers need to be **secure**. Only we should be able to ssh into them and do things. We should be able to run a web server on port 80 and make sure only people we want can reach it. Having to build a password database (remember to hash and salt and build a rigorous email recovery flow), or oauth integration, or fiddle with any other sort of auth and how it works with the language and framework you chose, is a huge distraction.

Other than that, it should just be a computer. We don’t need config files filled with options. It should be some kind of stock linux, the disk should be persistent, the disk should be fast. Setup should be an easy one-liner, that is scriptable. It should have a domain name. It should not, in isolation, cost some dollars a month and have dedicated resources, it should be a fully functional VM that shares CPU and RAM out of my fixed-price allotment.

_Just a computer._

Want to build a soccer scheduling app for your kids school? Want a box to try out agent-of-the-week on a project where it cannot trash your laptop’s dot files (or bug you for permission to `ls` every five seconds)? Run `ssh exe.dev new`

That is what exe.dev gives you. Pay a monthly fee for some compute resources. Spin up as many VMs as you like. Resource management and auth are taken care of for you.
