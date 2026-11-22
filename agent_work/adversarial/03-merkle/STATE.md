# Merkle independent checks — state

Goal/spec: complete ../../03-merkle/objstore-p2-03-merkle-adversarial.md.
Product revision: ad6c99738212d3985d06c9f19b25defb2e2464a6, chunk/03-merkle.
Status: COMPLETE — final run returned 70 HELD / 4 BROKEN / 0 setup errors in 76 seconds.

Done: single nested Go module, three independent scenario drivers, one supplemental
regression driver, aggregator, reduced reproductions and report. Final evidence is
in evidence/run-all.txt and evidence/final-run.stdout. No commits made.

In progress: nothing; no background process remains.
Next: coordinator reviews ATTACKS.md and dispatches any product repair or claim revision.
Open findings: C8 reply size; C12 batching trip bound; C21 worker prefix-reply trip
bound; C31 plan hash-only bound. Product repair or claim narrowing belongs to the
coordinator/builder; this brief authorizes testing only.

Resume: from this worktree root run bash agent_work/adversarial/03-merkle/run-all.sh.
Exit 1 is expected while the four reported claims remain broken. The report has
individual reproduction commands. All writes must stay in this directory.
