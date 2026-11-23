# Results — independent check of the README and the book's claims

One pass by Astra (gpt-6-astra) on commit 0910863 of the publishing-finish branch;
`FINDINGS.md` is its report, `WALKTHROUGH.md` and `steps.tsv` the command log, `CLAIMS.md`
the inventory of claims, `claims_test.go` two executable counterexamples.

- README walk on a clean clone: 36 commands (16 from the README, 3 from the site front
  page, 17 from the chapters), all exit 0. Three findings: the README did not state the
  host tools it needs (curl; jq for the full gate); two wait loops had no bound; two
  checkouts on one machine share a Docker project because `compose.yaml` fixes its name.
- Book: 381 claim groups checked, 13 not supported (B01–B13): 2 BLOCKER (false guarantees
  in the erasure-coding chapter), 2 MAJOR, 9 MINOR. No product defect; every finding is a
  sentence that said more than the code or the records support. 30 of 30 code excerpts
  matched the source; 380 of 380 relative links resolved; 106 of 106 named tests exist.

All 16 findings were accepted and closed in one prose round (Neon; report
`agent_work/08-book/FIX1-REPORT.md`, one row per finding with the evidence line read; none
disputed). The orchestrator read the diff against the findings, re-ran the site lint (0
findings) and the README command check on a clean clone (4 blocks pass). The pass was not
re-run after the fixes.

Re-run: `readme-walk.sh` (the walk) and `check-claims.sh` (the two counterexamples, which
PASS when they show the old wording was false). The Go test is its own nested module so the
root module's build and tests do not include it.

Not done by this pass (its own statement): historical sweeps, fuzz execution counts, race
runs and benchmarks were checked against the kept records, not re-run.
