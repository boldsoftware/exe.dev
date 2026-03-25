---
title: Teams
description: Shared VM management for your organization
subheading: "5. Teams"
suborder: 1
---

Teams let multiple people share a pool of VMs under one roof. Instead of
everyone managing their own account and quota separately, a team pools
resources and gives admins visibility into all team VMs.

With a team you get:

- **Shared VM pool.** All members' VMs count toward one shared quota instead
  of individual limits.
- **Admin access.** Team admins can SSH into any member's VM, and manage
  (rename, delete, copy) member VMs.
- **Team sharing.** Share a VM with the whole team in one command. New members
  automatically get access.
- **SSO.** Enforce Google OAuth or custom OIDC (Okta, Azure AD, etc.) for
  everyone on the team.

## Roles

Teams have three roles:

| Role | Can do |
|------|--------|
| **billing_owner** | Everything. Manages billing, can disband the team, configure SSO. |
| **admin** | Add/remove members, SSH into member VMs, transfer VMs. |
| **user** | Create and manage their own VMs, access team-shared VMs. |

A team always has exactly one billing owner. There can be multiple admins.

## Getting started

1. [Create a team and manage members](/docs/teams/members)
2. [Understand how VMs work in a team](/docs/teams/vms)
3. [Set up SSO](/docs/teams/sso) (optional)

For the CLI command reference, see [`team`](/docs/cli-team).
