---
title: "Some secret management belongs in your HTTP proxy"
description: do third-party integrations via HTTP header management
author: David Crawshaw
date: 2026-04-18
published: false
---

Agents fuss when you directly hand them an API key.
It usually works, and if you make it a rapidly revocable key that you
disable after the session, you mitigate the risks.
But some models (you know which ones) freak out on seeing the
secret, and refuse to do anything now that the key is “exposed.”
Models that are not so ridiculous about API keys will write the key
to inter-session memory, pulling it out in another session and burning
precious context window trying to use a revoked key.
All of which assumes you go to the effort of constantly generating keys.

Like so many problems getting attention right now, this looks like a
problem created by agents.
But the problem was always there.
API keys are convenient but too powerful.
Holding one does not just grant you the ability to make API calls,
it grants you the power to *give others the ability* to make API calls
(by sending them the key).
No software I write in production that has an /etc/defaults file full
of env vars containing API keys needs that power.
We have always just been careful about how we write programs to not
exfill keys.
Never careful enough, because many security flaws in such an app now
let the attacker walk off the keys and give them a window to do
nastiness from wherever they like, until we realize and start
manually rotating them.

Attempts to automate key rotation to close this hole have mixed
success.
Our industry does use OAuth in some places, and sometimes OAuth is
configured to rotate keys.
But services still ship API keys, because they are easy for users.
(OAuth, while simple in theory, is always painfully complex to use.)
Some services give us the worst of all worlds, like GitHub encouraging
personal access tokens with 90-day expiry windows.
Just long enough for you to forget about them and your internal service
to break mysteriously while you are on vacation.

Inter-server OAuth as commonly practiced today also does not help with
agents, as creation is usually designed to have some human intervention
via a web browser cookie in a way deliberately designed to be hard to
automate.
I do not think I have ever used a service that gave me an
OAUTH_CLIENT_SECRET via an API.
So it’s fine (if complex and painful) for traditional services, but
your agent is not doing that.

So in practice, what can we do today to solve this?

We can use an HTTP proxy that injects headers.

## Many secrets are HTTP headers

Many APIs talk HTTP. They usually ship an HTTP header, either a basic
auth header or their own.
Here is, for example, Stripe’s:

```
curl https://api.stripe.com/v1/customers \
  -u "sk_test_BQokikJOvBiI2HlWgH4olfQ2:" \
  -d "name=Jenny Rosen" \
  --data-urlencode "email=jennyrosen@example.com"
```

So instead of an /etc/defaults file with your sk_test key, if you have
an HTTP proxy managing secrets you can do this:

```
curl https://stripe.int.exe.xyz/v1/customers \
  -d "name=Jenny Rosen" \
  --data-urlencode "email=jennyrosen@example.com"
```

Where the server in the URL has been changed to another internal
service you run.
And the key has been removed! What grants your server, and your agents,
the ability to use the secret is their ability to reach your secrets
HTTP proxy.

This covers, amazingly, almost all secrets.

## _Integrations_ in exe.dev

The final piece of the puzzle is: why do you need to write and manage
an HTTP proxy?
Your cloud should do it for you.
So we built Integrations into exe.dev to do this.
Assign an integration to a tag, tag the VMs you want to have access, done.
Clone your VM, you get a fresh space to work with agents and your
integrations are automatically present.

<img src="/assets/exe-stripe-http.png" style="width: 600px; height: 600px;" alt="A screenshot of setting up an HTTP integration in exe.dev" />

For GitHub, we did something special, and built a GitHub App to manage
the OAuth for you.
No need for manual rotation of keys.
We intend to build a lot more integrations soon.
