# Logging

We use `slog` for structured logging. Always prefer structured attributes over string interpolation, and use `*Context()` variants (e.g. `slog.InfoContext(ctx, ...)`) to attach `trace_id` from context.

## Setup

`logging.SetupLogger(env, registry, attrs)` configures the default slog logger based on the deployment stage. It chains:
- A format handler (text, JSON, or tint) based on `LOG_FORMAT` env var or stage config
- Slack error notifications (if `SLACK_BOT_TOKEN` and stage config are set)
- OTEL log export (if `OTEL_EXPORTER_OTLP_ENDPOINT` is set)
- Trace ID injection from context (`tracing.NewHandler`)
- Log level metrics (if a Prometheus registry is provided)

## Canonical Log Lines

Following the [canonical log lines](https://stripe.com/blog/canonical-log-lines) pattern, each major operation emits a single wide event at completion, accumulating attributes throughout the request lifecycle.

### HTTP Requests (exed)

`log_type: http_request`, body: `"200: OK"`

Base attributes: `method`, `host`, `uri`, `local_addr`, `request_type` (proxy|terminal|web|gateway), `user_id`.

Proxy requests add: `proxy=true`, `vm_id`, `vm_name`, `vm_owner_user_id`, `exelet_host`, `route_port`, `route_share`, `proxy_shelley`.

Gateway requests (LLM proxy) add: `request_type=gateway`, `llm_model`, `vm_name`, `user_id`, `input_tokens`, `output_tokens`, `cache_creation_tokens`, `cache_read_tokens`, `cost_usd`, `remaining_credit_usd`, `conversation_id`, `shelley_version`.

Add attributes: `sloghttp.AddCustomAttributes(r, slog.String("key", val))`

### HTTP Requests (exeprox)

`log_type: http_request` on the exeprox dataset.

Base attributes: `method`, `host`, `uri`, `local_addr`.

Proxy requests add: `proxy=true`, `vm_name`, `vm_id`, `vm_owner_user_id`, `exelet_host`, `route_port`, `route_share`.

exeprox generates a `trace_id` for each request via `tracing.HTTPMiddleware` (or uses an incoming `X-Trace-ID` header).

### HTTP Requests (exelet)

`log_type: http_request` on the exelet dataset.

Attributes: `method`, `uri`, `vm_name`, `remote_ip`, `original_path`, `new_path`.

exelet only logs requests to its own HTTP endpoints (e.g. `/_/gateway`). Proxied HTTP traffic to VMs goes over SSH tunnel.

### SSH Connections to VMs

Body: `"SSH Connection to VM"`, `log_type: vm-ssh-connection`

Attributes: `user_id`, `conn_id`, `username`, `remote_addr`, `local_address`, `key_fingerprint`, `vm_name`, `vm_id`, `owner_user_id`, `container_id`, `instance_state`, `route`, `port`, `ctrhost`, `ssh_user`, `box_host`, `duration`.

The piper plugin uses `piperConnLog` (stored in context) to accumulate attributes into a single wide event.

Add attributes: `getPiperConnLog(ctx).add(slog.String("key", "val"))`

### SSH Connections to exed Shell

Body: `"SSH routing to exed shell"`, `log_type: ssh_proxy_auth`

Attributes: `user_id`, `username`, `remote_addr`, `local_address`, `key_fingerprint`, `duration`.

### SSH Handler Commands

Body: `"ssh command completed"`, `log_type: ssh_command`

Base attributes: `command`, `command_name`, `subcommand`, `rc`, `duration`, `user_id`.

Per-command attributes added via `CommandLogAddAttr(ctx, slog.Attr)`:
- new: `vm_name`, `vm_id`, `exelet_host`, `image`
- rm: `vm_name`
- rename: `vm_name`, `old_vm_name`, `new_vm_name`, `vm_owner_user_id`, `vm_id`
- restart: `vm_name`, `vm_id`, `vm_owner_user_id`
- resize: `vm_name`, `vm_id`, `vm_owner_user_id`

Commands from the web UI include `source=web`.

### gRPC Calls (exeprox -- exed)

Body: `"started call"` / `"finished call"`

Attributes: `grpc.service`, `grpc.method`, `grpc.code`, `grpc.time_ms`, `grpc.component` ("client" or "server"), `grpc.method_type`, `peer.address`.

Key gRPC methods for HTTP proxy requests:
- `ProxyInfoService/BoxInfo` — resolves hostname to VM
- `ProxyInfoService/TopLevelCert` — fetches TLS cert for custom domains
- `ProxyInfoService/UserInfo` — resolves VM owner
- `ProxyInfoService/CookieInfo` — resolves auth cookies

Client side (exeprox) logs at DEBUG; server side (exed) logs at INFO. Both carry the same `trace_id`.

### gRPC Calls (exelet)

Body: `"finished call"`, `log_type: grpc_request`

Attributes: `grpc.service`, `grpc.method`, `grpc.code`, `grpc.time_ms`, `grpc.component`, `grpc.method_type`, `peer.address`.

Handlers add: `container_id`, `vm_name` where available.

Add attributes: `logging.AddFields(ctx, logging.Fields{"key", val})`

## How to Add Attributes

| Context | Method |
|---------|--------|
| HTTP request (exed/exeprox/exelet) | `sloghttp.AddCustomAttributes(r, slog.String("key", val))` |
| SSH piper connection | `getPiperConnLog(ctx).add(slog.String("key", "val"))` |
| SSH command handler | `CommandLogAddAttr(ctx, slog.String("key", "val"))` |
| gRPC handler (exelet) | `logging.AddFields(ctx, logging.Fields{"key", val})` |
