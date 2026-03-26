# Local TLS

Run exed with TLS:

```
go run ./cmd/exed -stage=local -https=:443
```

In local stage, exed auto-starts a Pebble ACME server and issues certificates for `*.exe.cloud` subdomains (which resolve to 127.0.0.1). Browsers will warn about untrusted certs — this is expected since Pebble's CA is not in your trust store.

## Custom CNAME Testing

Custom domains work via CNAME records pointing to `*.exe.cloud` subdomains. Create a CNAME record:

```
testing.example.com.  CNAME  testing.exe.cloud.
```

Then visit `https://testing.example.com`. The untrusted-cert warning still applies.
