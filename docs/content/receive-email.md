---
title: Receive email
description: Receive emails to your VM
subheading: "2. Features"
---

Your VM can receive emails at `*@vmname.exe.xyz`.

## Enable

```bash
ssh exe.dev share receive-email vmname on
```

Once enabled, any email sent to `any.name.here@vmname.exe.xyz` will be delivered to `~/Maildir/new/` on your VM.

## Disable

```bash
ssh exe.dev share receive-email vmname off
```

Disabling does not delete existing emails.

## Email format

Emails are delivered in [Maildir format](https://en.wikipedia.org/wiki/Maildir).

Email include an injected `Delivered-To:` header as the first line, containing the envelope recipient address. Use this header (not `To:` or `CC:`) to determine what address the email was sent to.

To watch for new mail, poll or use inotify. For example:

```bash
inotifywait -m ~/Maildir/new -e create -e moved_to |
  while read dir action file; do
    FILE="$dir/$file"
    # process email in $FILE
    mv "$FILE" ~/Maildir/cur/
  done
```

You are responsible for promptly moving emails out of `~/Maildir/new/`. If there are more than 1000 files in that directory, we will automatically disable email receiving. If this happens, you may clear the backlog and re-enable it, as above.

## Limitations

- No spam, virus, phishing, or safety checks, we only deliver the bits
- No custom domains yet, only `*.exe.xyz`
- Strict receiving rules; mail that fails authentication may be rejected
- 1MB maximum message size
- Delivered emails must be processed promptly
