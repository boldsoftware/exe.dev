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

* SSH Connections

  "body: SSH Connection to VM"

  These indicate that we allowed a user to connect via the piper plugin.
  These don't have a meaningful duration, since they're not really at the "end"
  of anything, but are an important enough event.

* SSH Handler Commands

  "body: ssh command completed"
  (or)
  "log_type: ssh_command"

* gRPC "finished call"

  These aren't particularly well instrumented.


If you want to add some stat to a request, instead of logging it separately,
use `sloghttp.AddCustomAttributes(r, slog.String("pow_time_ms", timeMs))` or similar.






