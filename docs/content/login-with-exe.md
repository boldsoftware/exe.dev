---
title: Login with exe
description: Use exe.dev's authentication system in your applications
subheading: "2. Features"
suborder: 4
---

You can leverage exe.dev's authentication system to identify users accessing
your services through the [HTTP proxy](./proxy). This lets you build
authorization without managing passwords or e-mails yourself.

The "Login with exe" feature is complementary with [Sharing](./sharing).
If a site is public, all users can access it, and the developer
can implement their own authorization, including bouncing users through
the /\_\_exe.dev/login to require an e-mail address. Private sites always
have the authentication headers, because the site must have been shared
to be accessed.

## Authentication Headers

When a user is authenticated via exe.dev, the following headers are added to
requests coming into your box:

- `X-ExeDev-UserID`: A stable, unique user identifier
- `X-ExeDev-Email`: The user's email address
- `X-ExeDev-Role`: The user's relationship to the box: `owner`, `user`, or `anonymous`

The `X-ExeDev-Role` header is always present:

- `owner`: The authenticated user owns the box
- `user`: The authenticated user has been granted access (via email share or share link)
- `anonymous`: The request is unauthenticated (only possible on public proxies)

The `X-ExeDev-UserID` and `X-ExeDev-Email` headers are only present when the
user is authenticated. If your proxy is public, unauthenticated requests will
not have these headers but will have `X-ExeDev-Role: anonymous`.

## Special Authentication URLs

The following special URLs are available for authentication flows:

- **Login**: `https://boxname.exe.xyz/__exe.dev/login?redirect={path}`

  Redirects the user to log in, then returns them to the specified path.

- **Logout**: POST `https://boxname.exe.xyz/__exe.dev/logout`

  Logs the user out, removing the cookie for your domain.

## Development

If you're using an agent to develop on your exe.dev vm itself, your
server might be listening, for example, on http://localhost:8000/, and
nothing is providing these headers. Use an http proxy to add the
headers for testing. For example:

```
mitmdump \
  --mode reverse:http://localhost:8000 \
  --listen-port 3000 \
  --set modify_headers='/~q/X-Exedev-Email/user@example.com' \
  --set modify_headers='/~q/X-Exedev-Userid/usr1234'
```

## Example: nginx authorization

The following `nginx` configuration allows only specified email addresses to access a protected location:

```nginx
server {
    listen 80;
    server_name _;

    location / {
        # Check if X-ExeDev-Email header matches allowed addresses
        set $allowed "false";
        if ($http_x_exedev_email = "alice@example.com") {
            set $allowed "true";
        }
        if ($http_x_exedev_email = "bob@example.com") {
            set $allowed "true";
        }

        # Return 403 if not allowed
        if ($allowed = "false") {
            return 403 "Access denied. Please log in with an authorized account.";
        }

        # Serve content for authorized users
        root /var/www/html;
        index index.html;
        try_files $uri $uri/ =404;
    }
}
```
