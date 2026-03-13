# rummyd

**R**eal **U**ser **M**onitoring — **rummy**d.

rummyd checks that blog.exe.dev is rendering correctly by SSH'ing to each
production exeprox machine and fetching `https://blog.exe.dev/debug/gitsha`.
This verifies the full request path from the exeprox through to the blog
backend.

## Metrics

Exported at `/metrics` (default `:9099`):

- `rummy_blog_up{host}` — 1 if blog is reachable from this exeprox, 0 otherwise
- `rummy_blog_curl_latency_seconds{host}` — HTTP-only latency (curl `time_total`, excludes SSH overhead)
- `rummy_blog_total_latency_seconds{host}` — wall-clock latency including SSH connect + curl
- `rummy_blog_gitsha_info{host,sha}` — info metric with the deployed git SHA
- `rummy_checks_total{host,result}` — counter of checks performed
- `rummy_last_check_timestamp_seconds` — unix timestamp of the last check run

## Deployment

Runs on `mon`. Deploy with:

```
cd observability && ./deploy-rummyd.sh
```

## Alerts

- **Blog down on exeprox**: fires when `rummy_blog_up` is 0 for any host for 2 minutes. Routes to #buzz.
- **rummyd not running**: fires when `rummy_last_check_timestamp_seconds` hasn't updated in 5 minutes. Routes to #buzz.
