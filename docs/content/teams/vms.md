---
title: Team VMs
description: How VMs work within a team
subheading: "5. Teams"
suborder: 3
---

## Shared quota

When you're in a team, individual VM limits are replaced by team-wide limits.
All members' VMs count toward the same pool. Run `team` to see current usage:

```
VMs: 12 / 100
```

## Creating VMs

Team members create VMs the same way as individual users. The VM is owned by
whoever creates it and counts toward the team's shared quota.

## Admin visibility

Team admins (and the billing owner) see all team members' VMs when running
`ls -a`. These appear under a "Team VMs" section, separate from your own.

## Admin SSH access

Admins can SSH directly into any team member's VM, both by name and by IP
shard routing. This works the same as SSHing into your own box:

```
ssh mybox@exe.dev
```

Admins can also delete, rename, and copy member VMs.

## Sharing with the team

There are two kinds of team sharing.

### Web access

Share a VM's private web proxy with the whole team:

```
share add mybox team
```

Team shares are dynamic — when a new member joins, they automatically get
access to all team-shared VMs. Remove it with:

```
share remove mybox team
```

### SSH and Shelley access

Web sharing doesn't grant SSH or Shelley access. To let any team member
(not just admins) SSH into the VM or use Shelley on it:

```
share access allow mybox
```

Revoke with:

```
share access disallow mybox
```

See the [sharing docs](/docs/sharing) for more on how sharing works.
