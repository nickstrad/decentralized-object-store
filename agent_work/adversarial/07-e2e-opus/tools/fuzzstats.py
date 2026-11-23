#!/usr/bin/env python3
"""Summarise the per-execution lines written by the instr-fuzz-stats patch."""
import sys
rows = []
for line in open(sys.argv[1]):
    rows.append({k: int(v) for k, v in (kv.split("=") for kv in line.split())})
n = len(rows)
if n == 0:
    print("no executions recorded VIOLATION"); sys.exit()
s = lambda k: sum(r[k] for r in rows)
cnt = lambda f: sum(1 for r in rows if f(r))
viol = cnt(lambda r: r["migrated"] > 0 and r["moved_shards"] == 0) + cnt(lambda r: r["warm_ok"] < 2)
print("%d execs: warm-up objects acked %d (%.1f/exec); traffic puts to m %d acked of %d; execs with a migrate op %d, "
      "with a completed migration %d, only compensated %d; migrations completed %d compensated %d ambiguous %d; "
      "pre-existing objects moved %d, shards moved %d (%.1f per completed migration); execs completing a migration with 0 moved shards: %d%s"
      % (n, s("warm_ok"), s("warm_ok") / n, s("puts_m_ok"), s("puts_m"), cnt(lambda r: r["migops"] > 0),
         cnt(lambda r: r["migrated"] > 0), cnt(lambda r: r["migrated"] == 0 and r["compensated"] > 0),
         s("migrated"), s("compensated"), s("ambiguous"), s("moved_objects"), s("moved_shards"),
         s("moved_shards") / max(1, s("migrated")), cnt(lambda r: r["migrated"] > 0 and r["moved_shards"] == 0),
         " VIOLATION" if viol else ""))
