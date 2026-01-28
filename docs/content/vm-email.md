---
title: Send email
description: Send emails to yourself from your VM
subheading: "2. Features"
published: false
---

Your VM can send emails to you (the VM owner).

## Request

```bash
curl -X POST http://169.254.169.254/email/send \
  -H "Content-Type: application/json" \
  -d '{
    "to": "you@example.com",
    "subject": "Build Complete",
    "body": "Your build finished successfully!"
  }'
```

All JSON fields are required. `to` must be your email address.

Sending is rate-limited with a token bucket. If you accidentally flood yourself with emails (it happens to all of us), wait a few hours before trying again.

## Response

```json
{"success": true}
```

or

```json
{"error": "error message"}
```
