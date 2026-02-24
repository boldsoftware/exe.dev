We use "slog" for logging and do "structured logging". When possible, instead of interpolating
strings into your log line, add things as attributes.

Where possible, use *.WarnContext() vs *.Warn(), and so on. Passing along the context
enables logging to get a "trace_id" to attach to your log line, which helps piece
together log lines for a request, etc.

Following https://stripe.com/blog/canonical-log-lines or https://jeremymorrell.dev/blog/a-practitioners-guide-to-wide-events/,
it is useful to have a "canonical log line" at the end of an operation. Currently I am aware of the following
instances of that:

* HTTP Requests (exed)

  "log_type: http_request", body looks like "200: OK".

  Base attributes (all HTTP requests): method, host, uri, local_addr, request_type
  (proxy|terminal|web|gateway), user_id (when authenticated).

  Proxy requests additionally have: proxy=true, vm_id, vm_name, vm_owner_user_id,
  exelet_host, route_port, route_share (public|private), proxy_shelley (port 9999).

  Gateway requests (LLM proxy) additionally have: request_type=gateway,
  llm_model, vm_name, user_id, input_tokens, output_tokens, cache_creation_tokens,
  cache_read_tokens, cost_usd, remaining_credit_usd, conversation_id, shelley_version.

  Use `sloghttp.AddCustomAttributes(r, slog.String("key", val))` to add attributes.

* SSH Connections to VMs

  "body: SSH Connection to VM", "log_type: vm-ssh-connection"

  These indicate that we routed a user to a VM via the piper plugin.
  Attributes: user_id, conn_id, username, remote_addr, local_address,
  key_fingerprint, vm_name, vm_id, owner_user_id, container_id, instance_state,
  route (by_name, by_ip_shard, by_team_ip_shard), port, ctrhost, ssh_user, box_host,
  duration.

  The piper plugin uses `piperConnLog` (stored in context, like `CommandLog`)
  to accumulate attributes into a single wide event. Use
  `getPiperConnLog(ctx).add(...)` to add attributes.

* SSH Connections to exed shell

  "body: SSH routing to exed shell", "log_type: ssh_proxy_auth"

  These indicate SSH connections routed to the exed interactive shell
  (registration, interactive menu, etc.) rather than to a VM.
  Attributes: user_id, username, remote_addr, local_address, key_fingerprint, duration.

* SSH Handler Commands

  "body: ssh command completed", "log_type: ssh_command"

  These are emitted at the end of every SSH command execution.
  Base attributes: command (full command string), command_name (first word),
  subcommand (first two words), rc (exit code), duration, user_id.

  Command handlers add structured attributes via `CommandLogAddAttr(ctx, slog.Attr)`:
  - new: vm_name, vm_id, exelet_host, image
  - rm: vm_name (comma-separated if multiple)
  - rename: vm_name, old_vm_name, new_vm_name, vm_owner_user_id, vm_id
  - restart: vm_name, vm_id, vm_owner_user_id
  - resize: vm_name, vm_id, vm_owner_user_id

  Commands from the web UI include source=web.

* gRPC "finished call" (exelet)

  "body: finished call", "log_type: grpc_request"

  These are emitted by the gRPC logging interceptor on the exelet.
  Base attributes: grpc.service, grpc.method, grpc.code, grpc.time_ms,
  grpc.component, grpc.method_type, peer.address.

  Service handlers add: container_id, vm_name (where available via request
  or response data).

  Use `logging.AddFields(ctx, logging.Fields{"key", val})` (from
  go-grpc-middleware/v2/interceptors/logging) inside gRPC handlers.

* HTTP Requests (exelet)

  "log_type: http_request" on the exelet dataset.

  Attributes: method, uri, vm_name, remote_ip, original_path, new_path.


If you want to add some stat to a request, instead of logging it separately,
use `sloghttp.AddCustomAttributes(r, slog.String("pow_time_ms", timeMs))` or similar.

For SSH piper connections, use `getPiperConnLog(ctx).add(slog.String("key", "val"))`
to accumulate attributes that will be included in the canonical log line.

For SSH commands, use `CommandLogAddAttr(ctx, slog.String("key", "val"))` to add
attributes to the canonical "ssh command completed" line.
