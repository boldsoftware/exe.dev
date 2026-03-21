---
title: Custom Domains
description: Use your own domain with exe.dev
subheading: "2. Features"
suborder: 4
---

Point your own domain at your exe.dev VM. TLS certificates are issued automatically.
You'll need to visit your DNS provider's configuration to update these.

## Subdomains (CNAME)

For non-apex domains like `app.example.com`, create a CNAME record:

```
app.example.com  CNAME  vmname.exe.xyz
```

## Apex Domains (ALIAS + CNAME)

For apex domains like `example.com`, you need two DNS records.

1. **CNAME** record on `www` pointing to your VM:
   ```
   www.example.com  CNAME  vmname.exe.xyz
   ```

2. An **A** record on the apex pointing to the **IP** of `vmname.exe.xyz`. 
   However, many providers offer a convenient way to maintain this
   IP address dynamically, calling these types of records **ALIAS** or **ANAME**
   or **flattened CNAME**.
   ```
   # Lowest Common Denominator
   example.com  A  52.35.87.134
   # Cloudflare
   example.com  CNAME vmname.exe.xyz
   # Many others
   example.com  ALIAS vmname.exe.xyz
   ```

   The table below points you to the documentation for many common
   DNS providers.

   | Provider         | Mechanism | Documentation |
   | ---------------- | --------- | ------------- |
   | Cloudflare       | CNAME     | [docs](https://developers.cloudflare.com/dns/cname-flattening/) |
   | AWS Route 53     | ALIAS     | [docs](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/resource-record-sets-choosing-alias-non-alias.html) |
   | DNSimple         | ALIAS     | [docs](https://support.dnsimple.com/articles/alias-record/) |
   | Azure DNS        | ALIAS     | [docs](https://learn.microsoft.com/azure/dns/dns-alias) |
   | Google Cloud DNS | ALIAS     | [docs](https://cloud.google.com/dns/docs/records) |
   | Namecheap DNS    | ALIAS     | [docs](https://www.namecheap.com/support/knowledgebase/article.aspx/10128/2237/how-to-create-an-alias-record/) |
   | Porkbun DNS      | ALIAS     | [docs](https://kb.porkbun.com/article/68-how-to-edit-dns-records) |
   | DigitalOcean DNS | A         | [docs](https://docs.digitalocean.com/products/networking/dns/) |

## Cloudflare: Disable Proxy Mode or Configure Snippets

If you use Cloudflare for DNS, they tend to default you
to **Proxied** (orange cloud) rather than **DNS Only** (grey cloud).
Cloudflare's proxy replaces your desired CNAME/ALIAS targets
with Cloudflare IP addresses, and therefore breaks exe.dev's
custom domain support. To fix this, either disable their
proxy, or use Cloudflare Snippets (or Workers) to re-write
the request to point to `vmname.exe.xyz`. Snippets are a paid
feature.
