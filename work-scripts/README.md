# Reusable work scripts

- [Observability pipeline check](../agent_work/10-observability/check-otel.sh): use
  `--fixture` for A1's in-memory collector proof; no argument runs A2's real objnode
  proof after runtime/config wiring exists. It validates the pinned Collector config,
  checks service readiness, enforces a 60-second post-start deadline, archives service
  logs and query output, rejects enabled-test skips, and cleans up its own Docker project.
  Results are in `agent_work/10-observability/evidence/` (ignored generated files).

- [CSV analysis smoke](../agent_work/10-observability/check-csv.sh): after building
  `docker build --target dev-observability -t object-store-obs-analysis:stagec .`, flush
  two controlled batches through the Go CSV adapter and check the finalized segments
  with pinned DuckDB. Verifies unsigned precision above 2^53, labels, temp-file exclusion,
  counter deltas, request rate and mean latency. Set `OBS_ANALYSIS_IMAGE` to override
  the local image tag. This is a fixture proof, separate from real objnode acceptance.
- [Query finalized CSV](../deploy/observability/query-csv.sh): run the pinned DuckDB CLI
  in memory against a supplied CSV directory, optionally using a supplied SQL file.
