# Remaining observability build blocker

The frozen catalogue at core commit `bd3e01c` prevents plan-compliant erasure
snapshots. In `internal/metrics/catalogue.go`, the operation label domains of
`objstore_erasure_input_bytes_total` and `objstore_erasure_output_bytes_total`
contain the HTTP operations rather than `encode` and `reconstruct`.

A temporary probe passed four otherwise-valid batches through `metrics.Validate`.
Both required operation values were rejected for both metrics:

```text
objstore_erasure_input_bytes_total operation=encode: invalid label
objstore_erasure_input_bytes_total operation=reconstruct: invalid label
objstore_erasure_output_bytes_total operation=encode: invalid label
objstore_erasure_output_bytes_total operation=reconstruct: invalid label
```

Command: `go test -short ./internal/erasure -run '^TestO1FrozenErasureByteDomainProbe$' -v`.
The probe asserted these rejections and passed (0.007s); this demonstrates the defect,
not acceptance. The temporary test was removed. Its construction for each pair was:

```go
b := metrics.Batch{
    SchemaVersion: metrics.SchemaVersion, NodeID: "probe", Region: "local",
    BootID: "probe", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1,
    Snapshots: []metrics.Snapshot{{Component: "erasure", Points: []metrics.Point{{
        Name: name, Labels: []metrics.Label{{Name: "operation", Value: operation}},
        Counter: 1,
    }}}},
}
err := metrics.Validate(b)
```

The runner validates complete batches before export, so even first-batch zero-valued
erasure byte series would cause the whole batch to be dropped. Omitting those series
or using HTTP operation values would violate the plan.

Per Nick's explicit stop-on-core-blocker rule, Sol stopped all three build runs.
No core interfaces, catalogue, or selector were changed. The core owner needs to
correct the two domains and add regression tests before a fresh build resumes.

No product implementation commits were integrated. Partial Prometheus code/tests
remain uncommitted in `/root/Software/object-store-work/10-obs-adapters`; component
and runtime builds have no product changes. The worktrees are retained to preserve
work at the stop. All three run exits are 1 (interrupted), with logs and briefs at
`/root/runs/objstore-obs-{components,adapters,runtime}*`. No retry was launched,
because this is an explicit contract gate rather than a transient process failure.

Build/vet, complete fast-lane acceptance and independent review were not run for
the incomplete rest build. DuckDB remains analysis over CSV, per the existing plan;
no runtime DuckDB adapter was attempted. No push, main merge, or publication.

Durable knowledge assessment: unresolved predecessor defect recorded as a blocker,
not promoted to stable project knowledge. No reusable script was completed.
