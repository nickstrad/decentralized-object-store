# Runtime observability boundaries

The runtime has a dedicated unbuffered metrics inspection channel. Generic `Inspect`
uses the application-call cancellation state machine and can increment abandoned-call
counters; collection must not use that path. A metrics gate remains held until the owner
loop finishes the received request. A request canceled before the loop receives it leaves
no queued callback. Component snapshots are copied in one owner-loop turn; runtime and
HTTP atomics are sampled afterward. The returned batch is not a transaction across those
observations. `TestMetricsStalledLoopCancelsWithoutQueuedReadsOrAbandonment` pins this.

Objnode opens an adapter before starting the runtime/HTTP listeners, then runs one runner
with the same generated identity passed to the adapter. Shutdown cancels and joins the
runner, drains HTTP and runtime within one common budget, then closes the adapter. The
Prometheus startup rollback test verifies that HTTP bind failure releases an already-open
metrics listener. Invalid common configuration is rejected even when disabled; disabled
mode never opens the adapter or constructs/runs the collector runner.

HTTP operation labels mirror routing predicates. In particular only PUT with `?tag` is a
tag operation; GET/HEAD/DELETE with that query still operate on the object. Byte counters
observe actual body Read and response Write results. Content-Length rejection can happen
before any body read; use unknown-length/chunked requests to test MaxBytesReader reads.
Panics before a final HTTP response count as `other`, keep the in-flight gauge balanced,
and continue propagating the panic to the HTTP server.

The CLI help test reads each line's first column as its flag name. Treating every help
word as a flag incorrectly prohibited repeated prose words and failed when duration
settings added repeated defaults. No reusable script was needed for this unit; the
existing OTel pipeline check remains the integration entry point.
