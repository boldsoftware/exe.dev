---
title: Team SSO
description: Set up Google OAuth or OIDC for your team
subheading: "5. Teams"
suborder: 4
---

By default, team members log in with email and passkey. The billing owner can
enforce a different auth provider for the whole team.

## Viewing the current provider

```
$ team auth
Auth provider: default
```

## Google OAuth

To require all team members to sign in with Google:

```
team auth set google
```

That's it. Members will be redirected to Google's sign-in page.

## OIDC (Okta, Azure AD, etc.)

For a custom identity provider, use the `oidc` option. You'll need your
provider's issuer URL, client ID, and client secret.

```
team auth set oidc \
  --issuer-url=https://your-org.okta.com \
  --client-id=0oa1234567890 \
  --client-secret=your-secret-here \
  --display-name="Acme SSO"
```

exe.dev will run OIDC discovery against your issuer URL to validate the
configuration. On success, you'll see the callback URL:

```
Auth provider set to oidc
SSO issuer:    https://your-org.okta.com
Callback URL:  https://exe.dev/oauth/oidc/callback
```

**Set your IdP's redirect URI to the callback URL above.** This is the URL
your identity provider needs to redirect users back to after authentication.

### Updating OIDC settings

Run the same `team auth set oidc` command again with updated values. If you
want to keep the existing client secret, pass `--client-secret=***`.

The `--display-name` flag is optional and controls what's shown to users on
the login page.

## Resetting to default

To clear SSO and go back to email/passkey:

```
team auth set default
```

This removes any configured SSO provider.
