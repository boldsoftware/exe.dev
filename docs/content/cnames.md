---
title: Custom Domains
description: Use your own domain with exe.dev
subheading: "2. Features"
suborder: 2
---

Point your own domain at your exe.dev box. TLS certificates are issued automatically.
You'll need to visit your DNS provider's configuration to update these.

## Subdomains (CNAME)

For non-apex domains like `app.example.com`, create a CNAME record:

```
app.example.com  CNAME  mybox.exe.xyz
```

## Apex Domains (ALIAS + CNAME)

For apex domains like `example.com`, you need two DNS records:

1. **ALIAS** (or ANAME) record on the apex pointing to `exe.xyz`:
   ```
   example.com  ALIAS  exe.xyz
   ```

2. **CNAME** record on `www` pointing to your box:
   ```
   www.example.com  CNAME  mybox.exe.xyz
   ```