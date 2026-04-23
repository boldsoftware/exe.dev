---
title: Managing Members
description: Create a team, invite people, manage roles
subheading: "5. Teams"
suborder: 2
---

## Creating a team

Teams requires a paid plan. On a free/trial plan, both paths below will
tell you to upgrade first at [exe.dev/user](https://exe.dev/user).

There are two ways to create a team:

**From your profile page.** Visit [exe.dev/user](https://exe.dev/user).
If you're eligible, you'll see a **Teams** section with a "Create
Team" form. Enter a name and click **Create Team**.

**From SSH.** Run `team enable` from inside the exe.dev REPL — it's
interactive, so SSH in first rather than invoking it as a one-shot
command:

```
$ ssh user@exe.xyz
exe.dev ▶ team enable
Enable teams? (yes/no): yes
Team name: Acme Corp
Team Acme Corp created!
Use team add <email> to invite members.
```

Either way, you become the team's billing owner, and your existing VMs
become part of the team's shared pool.

## Inviting members

Admins and billing owners can invite people:

```
team add alice@example.com
```

This sends an invite email. How the invite works depends on whether the person
already has an exe.dev account:

- **Existing user:** They'll see the invite on their `/user` profile page and
  must explicitly accept it. When they join, their existing VMs become part of
  the team's shared pool and visible to team admins. If any of their VMs have
  IP shard collisions with existing team members, those shards get reassigned
  automatically.
- **New user:** The email contains a signup link. When they create their account
  through that link, they're automatically added to the team.

Invites expire after 24 hours.

## Listing members

```
team members
```

This shows all members and their roles. You can also use the alias `team ls`.

## Removing members

```
team remove alice@example.com
```

Members must delete all their VMs before they can be removed. This prevents
orphaned VMs from cluttering the team's quota.

## Transferring VMs

Admins can move a VM from one team member to another:

```
team transfer mybox alice@example.com
```

This changes ownership and clears all existing shares on the VM (both
individual and team shares). The new owner can re-share as needed.

## Viewing team info

Run `team` with no arguments to see a summary:

```
$ team
Team: Acme Corp
Your role: billing_owner
Members: 4
VMs: 12 / 100
```

## Disbanding a team

The billing owner can disband the team with `team disable`, but only after
all other members have been removed. This is also interactive — you'll be
asked to confirm.

Disabling a team:

- removes all team shares
- cancels pending invites
- removes team auth and SSO configuration
- deletes the team

Your VMs stay on your personal account.
