We use "slog" for logging and do "structured logging". When possible, instead of interpolating
strings into your log line, add things as attributes.

Where possible, use *.WarnContext() vs *.Warn(), and so on. Passing along the context
enables logging to get a "trace_id" to attach to your log line, which helps piece
together log lines for a request, etc.

Following https://stripe.com/blog/canonical-log-lines or https://jeremymorrell.dev/blog/a-practitioners-guide-to-wide-events/,
it is useful to have a "canonical log line" at the end of an operation. Currently I am aware of the following
instances of that:

* HTTP Requests.

  These can be identified with "log_type: http_request" and the body looks like "200: OK".
  (sloghttp can't modify this; I would have put the body as "http request", but whatever)

  These can be proxy: true/false, can be on the exelets, etc.
  (As of writing, exelet doesn't have http_request, but hopefully it will as of reading.)

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

  "body: ssh command completed"
  (or)
  "log_type: ssh_command"

* gRPC "finished call"

  These aren't particularly well instrumented.


If you want to add some stat to a request, instead of logging it separately,
use `sloghttp.AddCustomAttributes(r, slog.String("pow_time_ms", timeMs))` or similar.

For SSH piper connections, use `getPiperConnLog(ctx).add(slog.String("key", "val"))`
to accumulate attributes that will be included in the canonical log line.






