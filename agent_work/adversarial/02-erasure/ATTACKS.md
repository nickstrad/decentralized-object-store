# Chunk 2 adversarial report

The new finding is **C30: the stated Window bound does not bound outstanding PUT
requests**. With Window=1 and one paused holder, 2, 8 and 64 stripes respectively leave
2, 8 and 64 stripes pending at the successful PUT callback. This is a resource-bound /
claim-wording finding, not a wrong-byte finding. All other attacks in this harness held.
The already reported REVIEW-1 defects are excluded as detailed below.

Tested checkout: `265dc9c6359dedbbebc9f7ab29eaaaa8f452db6a`. All harness code, scripts,
results and this report live under `agent_work/adversarial/02-erasure/`. No product files,
root state files or git metadata were changed. No commits were made.

Run from the worktree root:

```bash
agent_work/adversarial/02-erasure/run-all.sh
```

Final run: `SUMMARY held=41 broken=1 setup_errors=0 elapsed_seconds=25`; exit status 1.

The authoritative output is [evidence/run-all.txt](evidence/run-all.txt). Each aNN driver
also runs independently with no arguments. Exit status is 0 for held attacks, 1 for a
broken claim, and 2 for setup failure. Docker/compiler diagnostics go to evidence files;
attack stdout contains one HELD/BROKEN line per attack. The nested module requires only
the local product module and the standard library. Builds use the required Docker dev
service, and temporary build directories are removed on exit. The final suite takes
well under one minute here, within the requested 20-minute budget.

## Numbered claims and attacks

C1–C29 number the build-notes claims table in its original order. C30 records the plan's
streaming / memory requirement and the explicit Window statement in the build notes.
“Held” below means these finite attacks held, not a proof over all inputs. Multiple
claims on one result line share one attack; summary counts are attack counts.

| Claim | Published assertion | Attacks actually executed and outcome |
|---|---|---|
| C1 | GF tables equal bitwise multiplication for every pair | `field-oracle`: all 65,536 products checked against independent polynomial reduction; HELD. |
| C2 | Golden products, inverses and powers for 0x11D | `field-oracle`: products plus all 256 inverse flags/values and 2,048 positive/negative exponent/log checks; HELD. |
| C3 | Field axioms and division | `field-oracle`: every division pair, zero-divisor rejection; `field-laws-slices`: 20,000 associativity/distributivity triples and unequal-length slice operations; HELD. |
| C4 | Two-sided matrix inverses; singular/non-square errors; input ownership | `matrix-hostile-copy`: row-swap and dense matrices, both inverse orders, singular/non-square/ragged/empty matrices, invalid selected rows, mutation of constructor input and returned Rows; HELD. Known negative Identity defect skipped. |
| C5 | Systematic Cauchy entries | `differential-encode`: 220 complete matrices for k=1..20,m=0..10 versus independent formula; `field-limit` exercises six n=256 layouts through independent encoding; HELD. |
| C6 | MDS for all k-row subsets | `exhaustive-loss-sets`: every loss mask for k=1..5,m=0..4; `field-limit`: eight exact-m-loss masks for each of six n=256 layouts; `random-loss-differential`: 300 midrange layouts and partial losses; HELD. |
| C7 | Parity equals independent vectors | `differential-encode`: 1,320 encodings, six lengths each, polynomial-only reference; `field-limit`: another 48 encodings across extreme layouts; HELD. |
| C8 | Valid/invalid config boundaries including m=0, n=256/257 | `field-limit`: (1,255),(2,254),(127,129),(128,128),(255,1),(256,0), and five invalid configurations; `differential-encode` also covers m=0 for k=1..20; HELD. Known huge-int overflow skipped. |
| C9 | Exact systematic round trip, boundaries and arbitrary recoverable erasures | Exhaustive small masks, 300 midrange trials, field-limit trials, 15 size boundaries including 3 MiB+17, plus 8 MiB+17 through Node; HELD. Both full and data-only reconstruction used. |
| C10 | Beyond m losses returns ErrTooFewShards without mutation/panic | `exhaustive-loss-sets`: every over-budget mask included in 1,922 total masks; deep comparison before/after failed reconstruction; HELD. |
| C11 | Hostile shard slices return sentinels without panic | `sizes-and-hostile-shards`: nil, wrong counts, unequal lengths, all missing across four APIs; `codec-sentinels`: wrong parity, bad lengths, invalid Join sizes, indexed errors; HELD. |
| C12 | Corruption becomes reported erasure; >m unavailable | Nine `shard-*` attacks each apply 1, 2, 3 damaged shards and assert exact bad-index count; flips, truncation, extension, swapped index/stripe/version, forgery, different code and length; HELD. Node rot/missing and region-wipe tests also held. |
| C13 | Self-consistent forgeries and foreign shapes rejected by manifest/header checks | `shard-forge`, `shard-code`, `shard-length`, `shard-index`, `shard-stripe`, `shard-version` all recompute valid SHA-256 via EncodeShard and then attack DecodeStripe with original manifest checksums; HELD at 1/2/3 damaged shards. |
| C14 | Shard decoder total, checksum/length exact, accepted encoding canonical | Every byte offset flipped in a 95-byte valid blob, every strict prefix, 10,000 random blobs, nine valid-origin mutation families; HELD. Known >64-MiB decode/re-encode mismatch deliberately skipped. |
| C15 | Chunk-store verification, idempotence, first write wins, repair, errors, durability | `store-model`: read I/O, conflict, identical Put, rot and repair, durable bytes; `store-identity-and-isolation`: 60 bucket/version namespaces, misnamed writes, full wipe and joined not-found; HELD. Known unsynced-existing retry defect skipped. |
| C16 | Chunk store matches a reference model | `store-model`: 1,000 deterministic operations, list count and every live key checked after every operation, durable-layer comparison and final crash; HELD. Additional namespace/wipe checks are independent. |
| C17 | Distinct, region-capped, deterministic placement; errors | `region-cap-adversarial`: 4,000 keys across eight region populations with independently calculated feasible cap and shuffled input order; `placement-invalid`: short, empty, duplicate, empty-ID and invalid count cases; `replace-cap-shrink`: valid distinct outputs; HELD. Known nil/mutating Membership defect skipped. |
| C18 | 3,000-stripe load on 7/7/6 nodes falls in [700,1100] | `placement-load`: independent stripe-key domain, 3,000 stripes, all 20 counts checked; min 812, max 1043, HELD. Region concentration separately challenged in 4,000 additional placements. |
| C19 | Only necessary holders/slots move when membership changes | `replace-churn`: 1,000 holder removals, additions and nonholder changes; `replace-cap-shrink`: 1,000 two-to-three-region changes move exactly two slots; `place-churn-sets`: up to 1,500 raw Place deletions over three region shapes, skipping cap changes; HELD. |
| C20 | Manifest canonical round trip and malformed/invalid errors | `manifest-canonical`: six sizes, every prefix, trailing bytes, nonminimal varint, negative size; `manifest-hostile-numbers`: 20,000 magic-prefixed random inputs including maximal uint64 varints; HELD. |
| C21 | Node exact put/get, durable manifest placements, clean k reads | `node-clean-boundaries`: 480 GETs, all 20 nodes, eight object sizes, windows 1/4/9; every manifest shard checked durable and checksum-equal; HELD. Region rules independently checked by placement and region-loss tests. |
| C22 | Rot triggers parity fallback; >m returns ObjectError with remote corruption | `node-fault-rot`: 12 seeds, 2/3 rotten shards per stripe, three readers, exact bytes or expected ObjectError/ErrUnavailable/ErrCorrupt/ErrRemote; HELD. `shard-flip` independently checks decoder bad-index reporting. |
| C23 | k+1 quorum succeeds early, fewer fail | `quorum-and-slow-holder`: one or two paused holders and one or two crashed holders, callbacks exactly once; one-holder loss acknowledges before 1-second timeout, two fail; HELD. Window probe independently observes success with one paused holder. Known placement-error reentrancy skipped. |
| C24 | Transient retries succeed; persistent faults fail with I/O and bounded requests | `node-fault-read-once` and `node-fault-read-persistent`: each 72 GETs over 12 seeds and 2/3 damaged shards per stripe; request ceiling 24 per stripe, exact bytes or appropriate I/O failure; HELD. |
| C25 | Content hash catches incorrect manifests | `content-hash-and-forged-manifest`: wrong hash at sizes 0/1/129, plus forged payloads with self-consistent shard checksums AND updated manifest shard checksums while retaining original object hash; all rejected as ErrCorrupt without data; HELD. |
| C26 | Storage sentinels survive wire mapping; ObjectError wraps/messages | `wire-and-object-errors`: five codes, direct/wrapped/joined errors, ObjectError, all three request operations, truncated requests; Node rot and I/O attacks separately verify actual remote causes; HELD. |
| C27 | Within-budget DST never returns wrong bytes/hangs; permanent damage recovers | Fault matrices at seeds 220211..220222; region wipe/partition at seeds 220211..220220; `scheduled-fault-replay`: ten overlapping GETs amid 34 timed rotations, twice with identical digests; HELD. This is independent coverage, not a rerun of all 500 published seeds. |
| C28 | Beyond-budget corruption always unavailable; otherwise exact or unavailable | Three rotten or deleted shards in every stripe, 12 seeds/three readers; no returned bytes on failure; within-budget two-shard and full-region cases also checked; HELD. |
| C29 | Checksum-disabled canaries really fail their underlying checks | `checksum-canaries`: runs both existing test-only plant tests in Docker, checks both PASS markers as well as exit 0; property canary checks normal and planted modes and DST canary checks the expected corruption reason; HELD. These two tests intentionally reuse the product test hooks. |
| C30 | Multi-MB streaming and at most Window stripes in flight | `multimb-memory`: 8 MiB+17 exact round trip, ~120 MB total allocated, HELD against a diagnostic 1-GiB ceiling. `window-pending-stripes`: Window=1 leaves 2/8/64 outstanding stripes, BROKEN under the advertised outstanding-work interpretation. |

## Independent oracle and statistical tolerances

`main.go` implements polynomial multiplication by shift/reduce with modulus 0x11D,
inversion by exponentiation to 254, the systematic Cauchy generator, and a decoder that
solves an augmented matrix containing the selected payload columns. None of these
reference functions call production GF, Matrix or Codec functions. This is an independent
reference implementation in the harness module, not an alternate wrapper around the
product. It uses the specified Cauchy matrix deliberately; a default Vandermonde codec's
parity bytes would not be a valid byte-for-byte oracle for this format.

Every byte check is exact. Exhaustive loss tests include all 1,922 masks in their stated
small domain, not only randomly chosen erasures. Field-limit sampling uses eight seeds
220211..220218 for each of six layouts; middle-range sampling uses 300 draws from seed
220211 with k in 1..64 and m in 0..64. These are finite samples, not a statistical proof
of MDS at every layout. The field oracle checks all 65,536 products/divisions exactly.

Placement load uses the published absolute tolerance [700,1100], N=3,000 stripes and
18,000 shard placements. With the cap of two in each of three regions, expected per-node
counts are 6000/7 (~857.14) or 6000/6 (1000); I do not assume 900 for every node. Observed
min/max are 812/1043. No p-value or independence claim is made. Region-cap and movement
checks use exact integer limits. Multi-MB allocation uses TotalAlloc around a whole
simulated store round trip; the simulator stores/clones disk and network bytes, so this
is not a measure of codec-only memory or peak RSS. The 1-GiB ceiling is a deliberately
loose diagnostic chosen before execution, not a published product guarantee.

## BROKEN C30: outstanding PUT stripes escape Window

Minimal observed reproduction: seed **220211**, six nodes n000..n005 in three regions,
K=4, M=2, StripeSize=4, Window=1, default AckQuorum=5 and RequestTimeout=1 second;
network latency exactly 1 microsecond both within and across regions; pause n005 after
cluster startup; PUT a 5-byte payload from n000. The helper's payload is deterministic
(`payload(5)`); its contents are irrelevant to the pending-request count.

Run the isolated attack from the worktree root:

```bash
bash agent_work/adversarial/02-erasure/driver.sh node window-pending-stripes
```

Actual result:

```text
BROKEN C30 window-pending-stripes: seed=220211 Window=1, k=4,m=2,stripe=4,pause n005: pending stripes at PUT ack=[2 8 64] for 2/8/64 stripes; smallest reproduction is 5 bytes, seed=220211
```

The attack advances only until the PUT callback succeeds, before any paused-holder
request times out. It obtains the originating Node's exported `Stats().Requests` and
subtracts the sum of all six nodes' `Stats().Served`. There are no other requests. Each
stripe has all six nodes as holders, all five live holders have served their successful
requests, and n005 has served none. Thus the difference is exactly the number of distinct
stripes with an outstanding request at n005. The three experiments yield 2, 8 and 64,
although Window is always one. The five-byte/two-stripe case suffices to falsify the
literal bound; the other cases demonstrate growth.

The implementation advances the window when a stripe reaches quorum, leaving the other
requests outstanding. Queued requests and their payloads are therefore not bounded by
Window. This does **not** contradict C23's requirement to acknowledge without waiting
for slow holders, and the harness does not demand delaying that acknowledgment. It
contradicts using the Window value as an outstanding-stripe / resource bound. If “in
flight” is intended to mean only “not yet at quorum,” narrow the documentation explicitly
and describe outstanding request buffering separately. Alternatively enforce a separate
outstanding-work bound while preserving the intended acknowledgment semantics. No OOM,
leak, or wrong-byte behavior is claimed by this reproduction.

## Plan attack-list coverage

- Independent differential encoder AND decoder: `differential-encode`,
  `exhaustive-loss-sets`, `random-loss-differential`, `field-limit`.
- Wrong bytes versus errors for (k,m,loss set): exhaustive small masks, midrange random
  masks, 256-shard extremes, hostile slice/sentinel checks.
- Header and checksum still agree: `shard-forge`, `shard-code`, `shard-length`,
  `content-hash-and-forged-manifest` use EncodeShard to create self-consistent envelopes.
- Swap stripes / objects: `shard-stripe` and `shard-version` create foreign identities
  with valid checksums; `shard-index` changes the slot identity; store rejects misnaming.
- Truncated / oversized shards: strict prefixes, one-byte extensions and self-consistent
  longer payloads; DecodeStripe rejects wrong expected lengths.
- Field limit: six n=256 layouts, including 1+255 and 256+0.
- Multi-MB memory: 3-MiB codec boundary, 8-MiB Node allocation measurement, pending-stripe
  bound with a paused holder.
- Placement skew / region concentration: eight populations, 4,000 placements, 3,000
  load-distribution keys, region-loss recovery, cap-shrink and membership churn.

## Exclusions and limits

Read REVIEW-1 before selecting attacks. Deliberately did not reproduce its unsynced
identical-Put retry, placement-error synchronous callback, k+m integer overflow, skipped
fuzz seeds, nil/mutating Membership, negative Identity / overflowing ShardSize,
checksum-valid >64-MiB DecodeShard acceptance, or mislabeled benchmark. Related valid
input and unrelated malformed-input paths remain covered. Per-bucket codec/version
integration is explicitly deferred to Chunk 6, so it is not scored broken.

`node-fault-wipe` deletes individual shard blobs durably through the exported Store API
to keep damage at exactly two or three per stripe; it does not pretend to be a physical
whole-disk wipe. `region-loss-and-wipes` uses the actual simulator WipeDisk on all nodes
of one region. Every fault campaign starts after all initial shard writes have drained
and are durable; it does not conflate quorum acknowledgment with all n shards being
present. The published transient-I/O exception is respected.

The full 500-seed nemesis workload, every possible field-limit subset, a long memory
soak, real filesystem fsync faults and HTTP streaming are outside this harness. Node
and storage use the frozen simulator; code/math checks are pure computation. Random
malformed data is finite deterministic mutation coverage, not coverage-guided fuzzing.
The canary plant is test-only and inaccessible to a nested-module import, so its existing
public test entry points are invoked instead of copying or modifying production code.

An initial environment lookup (`kb search ... --caller codex`) failed because this
worktree's configured KB index did not exist; it produced no search ID to judge. No KB
index or external files were created. The brief and checked-in compose/simulator
contracts supplied everything needed to execute the harness.
