# Reusable work scripts

- [Observability pipeline check](../agent_work/10-observability/check-otel.sh): use
  `--fixture` for A1's in-memory collector proof; no argument runs A2's real objnode
  proof after runtime/config wiring exists. It validates the pinned Collector config,
  checks service readiness, enforces a 60-second post-start deadline, archives service
  logs and query output, rejects enabled-test skips, and cleans up its own Docker project.
  Results are in `agent_work/10-observability/evidence/` (ignored generated files).
