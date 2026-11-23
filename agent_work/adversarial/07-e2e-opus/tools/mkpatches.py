#!/usr/bin/env python3
"""Regenerate mutations/*.patch from exact string replacements on the tracked tree.

Usage (from the worktree root): python3 -I agent_work/adversarial/07-e2e-opus/tools/mkpatches.py
Each mutation is a list of (path, old, new) edits; `old` must occur exactly once
(old=None creates a new file with content `new`). Fails loudly if the target code
has moved, so a stale mutation is a setup error, never a silent no-op.
"""
import os, subprocess, sys, tempfile, shutil

ROOT = os.getcwd()
OUT = os.path.join(ROOT, "agent_work/adversarial/07-e2e-opus/mutations")

NODE_OBJ = "internal/node/objects.go"
MIG = "internal/node/migrate.go"
NEM = "test/e2e/nemesis_test.go"
E7 = "test/e2e/e7_test.go"

# Helper inserted into internal/node/objects.go by the E2 partition mutations: how many
# regions the node's live view holds (3 = whole, 2 = majority side, 1 = minority side).
REGIONS_HELPER = '''
func (n *Node) mutRegions() int {
	seen := map[env.Region]bool{}
	for _, m := range n.view.Members() {
		seen[m.Region] = true
	}
	return len(seen)
}

var mutPostSplit = map[string]bool{}

var mutStale = map[string]objectRecord{}

func (n *Node) GetObject('''

def get_gate(cond):
    """GetObject answers ErrUnavailable when cond holds (cond is Go over n, bucket, key)."""
    return [(NODE_OBJ, "\nfunc (n *Node) GetObject(", REGIONS_HELPER),
            (NODE_OBJ, "\toi, rec, err := n.head(bucket, key)\n\tif err != nil {\n\t\tfail(err)\n\t\treturn\n\t}\n\tn.store.Get(",
             "\toi, rec, err := n.head(bucket, key)\n\tif err == nil && (%s) {\n\t\terr = fmt.Errorf(\"mutant: get while split: %%w\", storage.ErrUnavailable)\n\t}\n\tif err != nil {\n\t\tfail(err)\n\t\treturn\n\t}\n\tn.store.Get(" % cond)]

def put_gate(cond, helper=True):
    e = []
    if helper:
        e.append((NODE_OBJ, "\nfunc (n *Node) GetObject(", REGIONS_HELPER))
    e.append((NODE_OBJ, "\tif err == nil && !bs.writable() {\n\t\terr = fmt.Errorf(\"node: bucket %q is moving: %w\", req.Bucket, storage.ErrBusy)\n\t}",
              "\tif err == nil && !bs.writable() {\n\t\terr = fmt.Errorf(\"node: bucket %%q is moving: %%w\", req.Bucket, storage.ErrBusy)\n\t}\n\tif err == nil && (%s) {\n\t\terr = fmt.Errorf(\"mutant: put while split: %%w\", storage.ErrUnavailable)\n\t}" % cond))
    return e

SPLIT = "n.mutRegions() < 3"
MINORITY = "n.mutRegions() == 1"
MAJORITY = "n.mutRegions() == 2"

M = {}

# ---------------- Part 1.1: the fuzzed migrating bucket ----------------
M["p11a-fuzz-unplaceable-9x42"] = [(E7, "\to.nodes = 12\n\to.cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: simStripe}",
                                    "\to.nodes = 9\n\to.cfg.Layout = storage.Layout{K: 4, M: 2, StripeSize: simStripe}")]
M["p11b-fuzz-unplaceable-6x21"] = [(E7, "\to.nodes = 12\n", "\to.nodes = 6\n")]
M["p11c-routed-put-ignores-region"] = [("internal/node/node.go", "func (n *Node) scope(region string) *storage.Node {\n\tif region == \"\" {",
                                        "func (n *Node) scope(region string) *storage.Node {\n\tif true || region == \"\" {")]
M["p11d-routed-bucket-quota-1"] = [(NEM, "\tif err := createBucket(c, simBounds, storage.BucketSpec{Name: bucketM, Region: n.region}); err != nil {\n\t\treturn err\n\t}",
                                    "\tif err := createBucket(c, simBounds, storage.BucketSpec{Name: bucketM, Region: n.region}); err != nil {\n\t\treturn err\n\t}\n\tif q := c.SetQuota(c.nodes[0], bucketM, 1<<40, 1); c.Wait(q) != nil || q.Err != nil {\n\t\treturn failf(\"mutant quota: %v\", q.Err)\n\t}\n\tif _, err := c.Await(\"mutant quota replicated\", nemHealBound, c.Converged); err != nil {\n\t\treturn err\n\t}")]
M["p11e-routed-put-unavailable-after-2"] = [(NODE_OBJ, "\tif err == nil && !bs.writable() {\n\t\terr = fmt.Errorf(\"node: bucket %q is moving: %w\", req.Bucket, storage.ErrBusy)\n\t}",
                                              "\tif err == nil && !bs.writable() {\n\t\terr = fmt.Errorf(\"node: bucket %q is moving: %w\", req.Bucket, storage.ErrBusy)\n\t}\n\tif err == nil && bs.route != \"\" && len(n.liveObjects(rd, req.Bucket)) >= 2 {\n\t\terr = fmt.Errorf(\"mutant: routed bucket full: %w\", storage.ErrUnavailable)\n\t}")]
M["p11f-copy-writes-nothing"] = [(MIG, "\t\t\tcp.Put(m.Bucket, o.key, res.Data, func(man *storage.Manifest, err error) {",
                                  "\t\t\tfunc(_ string, _ string, _ []byte, f func(*storage.Manifest, error)) { f(o.rec.Manifest, nil) }(m.Bucket, o.key, res.Data, func(man *storage.Manifest, err error) {")]
M["p11g-retire-purge-noop"] = [(MIG, "\t\t\tg.purge(m.Bucket, g.n.deadVersions(rd, m.Bucket), func(err error) {",
                                "\t\t\tfunc(_ string, _ []storage.VersionID, f func(error)) { f(nil) }(m.Bucket, g.n.deadVersions(rd, m.Bucket), func(err error) {")]
M["p11h-copy-skips-last-and-catchup-verify-noop"] = [
    (MIG, "\t\tif i == len(objs) {\n\t\t\tfinish(nil)", "\t\tif i == len(objs) || (len(objs) > 1 && i == len(objs)-1) {\n\t\t\tfinish(nil)"),
    ("internal/saga/migration.go", "\t\t\t\t\t\tif !bytes.Equal(src, dst) {", "\t\t\t\t\t\tif false && !bytes.Equal(src, dst) {")]
M["p11i-copy-skips-last"] = [(MIG, "\t\tif i == len(objs) {\n\t\t\tfinish(nil)", "\t\tif i == len(objs) || (len(objs) > 1 && i == len(objs)-1) {\n\t\t\tfinish(nil)")]
M["p11j-retire-never-marks-replaced-dead"] = [(MIG, "\t\t\t\tif oerr == nil {\n\t\t\t\t\tg.n.markDead(m.Bucket, old.Manifest.Version)\n\t\t\t\t}",
                                               "\t\t\t\tif oerr == nil && false {\n\t\t\t\t\tg.n.markDead(m.Bucket, old.Manifest.Version)\n\t\t\t\t}")]
# instrumentation (not a mutation): log one line of nemesis stats per fuzz execution.
M["instr-fuzz-stats"] = [(E7, "\t\ts, _ := runFuzzInput(fuzzOptions(), fuzzWarmM, data)\n\t\tif err := s.Verify(); err != nil {",
                          "\t\ts, st := runFuzzInput(fuzzOptions(), fuzzWarmM, data)\n\t\topusFuzzLog(data, st)\n\t\tif err := s.Verify(); err != nil {"),
                         ("test/e2e/opus_instr_test.go", None, open(os.path.join(ROOT, "agent_work/adversarial/07-e2e-opus/tools/opus_instr_test.go.txt")).read())]

# ---------------- Part 1.2: migration start with unknown outcome ----------------
M["p12a-forget-on-unreachable"] = [(NEM, "\t\tswitch {\n\t\tcase errors.Is(start.Err, env.ErrTimeout) || errors.Is(start.Err, env.ErrUnreachable):",
                                     "\t\tswitch {\n\t\tcase errors.Is(start.Err, env.ErrUnreachable):\n\t\t\treturn nil\n\t\tcase errors.Is(start.Err, env.ErrTimeout) || errors.Is(start.Err, env.ErrUnreachable):")]
M["p12b-no-refresh-after-migration"] = [(NEM, "\tif _, err := c.Await(\"convergence after migration\", nemHealBound, c.Converged); err != nil {\n\t\treturn err\n\t}\n\tif err := n.refreshRegion(); err != nil {\n\t\treturn err\n\t}",
                                          "\tif _, err := c.Await(\"convergence after migration\", nemHealBound, c.Converged); err != nil {\n\t\treturn err\n\t}\n\tif mi.State == \"completed\" {\n\t\tn.region = wantTo\n\t}")]
M["p12c-skip-outside-check"] = [(NEM, "\t_, err = c.Await(\"no shard of the migrated bucket outside its region\", nemHealBound, func() (bool, error) {\n\t\tleft, err := shardsOutside(c, bucketM, n.region)\n\t\treturn len(left) == 0, err\n\t})",
                                  "\t_, err = c.Await(\"no shard of the migrated bucket outside its region\", nemHealBound, func() (bool, error) {\n\t\treturn true, nil\n\t})")]
M["p12d-unblock-coordinator-wipe"] = [(NEM, "\t\tif op.kind == opWipeNode && n.mig != nil && n.migCoord.ID == string(id) {\n\t\t\treturn skip()\n\t\t}\n", "")]
M["p12e-no-saga-lookup"] = [(NEM, "\tfor _, id := range nd.Engine().IDs() {\n\t\tif !n.seenSagas[id] {\n\t\t\treturn id, true\n\t\t}\n\t}\n\treturn \"\", false",
                              "\tfor _, id := range nd.Engine().IDs() {\n\t\tif false && !n.seenSagas[id] {\n\t\t\treturn id, true\n\t\t}\n\t}\n\treturn \"\", false")]
M["p12f-saga-claim-never-completes"] = [(MIG, "\tg.n.later(func() { done(g.claim(c, m)) })", "\tg.n.later(func() {\n\t\tif err := g.claim(c, m); err != nil {\n\t\t\tdone(err)\n\t\t}\n\t})")]
M["p12g-skip-per-key-and-outside"] = M["p12c-skip-outside-check"] + [(NEM, "\t\t\tif have < c.cfg.Layout.K {", "\t\t\tif false && have < c.cfg.Layout.K {"),
                                                                   (NEM, "\t\tif n.warmM > 0 && (len(preKeys) < n.warmM || shards == 0) {", "\t\tif false && n.warmM > 0 && (len(preKeys) < n.warmM || shards == 0) {")]

# ---------------- Part 1.3: partition-time reads and writes in E2 ----------------
M["p13a-get-and-put-unavailable-while-split"] = get_gate(SPLIT) + put_gate(SPLIT, helper=False)
M["p13b-get-unavailable-while-split"] = get_gate(SPLIT)
M["p13c-put-unavailable-while-split"] = put_gate(SPLIT)
M["p13d-minority-side-unavailable"] = get_gate(MINORITY) + put_gate(MINORITY, helper=False)
M["p13e-majority-side-unavailable"] = get_gate(MAJORITY) + put_gate(MAJORITY, helper=False)
M["p13f-get-unavailable-for-pre-cut-keys"] = get_gate(SPLIT + " && !mutPostSplit[bucket+\"/\"+key]") + [
    ("internal/node/node.go", "func New(e env.Env, cfg Config) (*Node, error) {\n", "func New(e env.Env, cfg Config) (*Node, error) {\n\tif e.ID == \"n000\" || len(mutStale) > 4096 {\n\t\tmutStale = map[string]objectRecord{}\n\t\tmutPostSplit = map[string]bool{}\n\t}\n"),
    (NODE_OBJ, "\tkey := occ.ManifestKey(req.Bucket, req.Key)\n\tvar pre occ.Precondition",
     "\tkey := occ.ManifestKey(req.Bucket, req.Key)\n\tif n.mutRegions() < 3 {\n\t\tmutPostSplit[req.Bucket+\"/\"+req.Key] = true\n\t}\n\tvar pre occ.Precondition")]
M["p13g-tier-r-only-unavailable-while-split"] = get_gate(SPLIT + " && mutTierR") + put_gate(SPLIT + " && mutTierR", helper=False) + [
    ("internal/node/mut_tierr_on.go", None, "//go:build e2efault\n\npackage node\n\nconst mutTierR = true\n"),
    ("internal/node/mut_tierr_off.go", None, "//go:build !e2efault\n\npackage node\n\nconst mutTierR = false\n")]
M["p13h-stale-read-while-split"] = [(NODE_OBJ, "\nfunc (n *Node) GetObject(", REGIONS_HELPER),
    ("internal/node/node.go", "func New(e env.Env, cfg Config) (*Node, error) {\n", "func New(e env.Env, cfg Config) (*Node, error) {\n\tif e.ID == \"n000\" || len(mutStale) > 4096 {\n\t\tmutStale = map[string]objectRecord{}\n\t\tmutPostSplit = map[string]bool{}\n\t}\n"),
    (NODE_OBJ, "\tkey := occ.ManifestKey(req.Bucket, req.Key)\n\tvar pre occ.Precondition",
     "\tkey := occ.ManifestKey(req.Bucket, req.Key)\n\tif n.mutRegions() < 3 {\n\t\tif o, _, oerr := n.object(rd, key); oerr == nil {\n\t\t\tif _, ok := mutStale[req.Bucket+\"/\"+req.Key]; !ok {\n\t\t\t\tmutStale[req.Bucket+\"/\"+req.Key] = o\n\t\t\t}\n\t\t}\n\t}\n\tvar pre occ.Precondition"),
    (NODE_OBJ, "\toi, rec, err := n.head(bucket, key)\n\tif err != nil {\n\t\tfail(err)\n\t\treturn\n\t}\n\tn.store.Get(",
     "\toi, rec, err := n.head(bucket, key)\n\tif err == nil && n.mutRegions() < 3 {\n\t\tif o, ok := mutStale[bucket+\"/\"+key]; ok {\n\t\t\trec = o\n\t\t\toi.Size, oi.ETag, oi.Version = o.Manifest.Size, \"\", o.Manifest.Version.String()\n\t\t}\n\t}\n\tif err != nil {\n\t\tfail(err)\n\t\treturn\n\t}\n\tn.store.Get(")]
M["p13i-wrong-bytes-while-split"] = [(NODE_OBJ, "\nfunc (n *Node) GetObject(", REGIONS_HELPER),
    (NODE_OBJ, "\t\tn.stats.Gets++\n\t\tcb(oi, res.Data, nil)", "\t\tn.stats.Gets++\n\t\tif n.mutRegions() < 3 && len(res.Data) > 0 {\n\t\t\td := append([]byte(nil), res.Data...)\n\t\t\td[len(d)/2] ^= 0x01\n\t\t\tres.Data = d\n\t\t}\n\t\tcb(oi, res.Data, nil)")]

# ---------------- Part 2: other finish items and the oracles ----------------
M["p2a-health-discards-zero"] = [(NODE_OBJ, "\th.Discards = n.occ.LWWDiscards()", "\th.Discards = 0")]
M["p2b-get-error-misclassified"] = [(NODE_OBJ, "\t\t\tif inv, ok := n.inv[bucket]; ok && isCorrupt(err) {\n\t\t\t\tinv.full = true\n\t\t\t}\n\t\t\tcb(oi, nil, err)",
                                     "\t\t\tif inv, ok := n.inv[bucket]; ok && isCorrupt(err) {\n\t\t\t\tinv.full = true\n\t\t\t}\n\t\t\tcb(oi, nil, fmt.Errorf(\"mutant: %v: %w\", err, storage.ErrInvalid))")]
M["p2c-blocklist-skips-equal-size"] = []  # filled below if the code matches
M["p2d-spread-cap-ignored"] = [("internal/storage/placement.go", "\t\tif rc.counts[i] >= capacity {\n\t\t\tcontinue\n\t\t}\n\t\trc.counts[i]++\n\t\tout = append(out, c.ID)\n\t}\n\treturn out, nil\n}\n\n// Replace",
                                "\t\tif false && rc.counts[i] >= capacity {\n\t\t\tcontinue\n\t\t}\n\t\trc.counts[i]++\n\t\tout = append(out, c.ID)\n\t}\n\treturn out, nil\n}\n\n// Replace")]
M["p2e-collect-never-deletes"] = [("internal/node/inventory.go", "\tif err := n.store.Store().Delete(id); err != nil && !isNotFound(err) {\n\t\tn.logf(\"collect %s: %v\", name, err)",
                                   "\tif err := error(nil); false && n.store.Store().Delete(id) != nil && !isNotFound(err) {\n\t\tn.logf(\"collect %s: %v\", name, err)")]
M["p2f-loss-declared-on-one-absence"] = [("internal/node/inventory.go", "\t\tif c > man.Layout.M {\n\t\t\treturn s, c, true", "\t\tif c > 0 {\n\t\t\treturn s, c, true")]
M["p2g-minority-tag-acked-not-written"] = [(NODE_OBJ, "\nfunc (n *Node) GetObject(", REGIONS_HELPER),
    (NODE_OBJ, "\theld := rd.Record(k)\n\tv, err := n.occ.PutTag(bucket, key, name, value, pre)",
     "\theld := rd.Record(k)\n\tif n.mutRegions() == 1 && held.Live() {\n\t\treturn storage.TagInfo{Name: name, Value: value, ETag: held.ETag().String()}, nil\n\t}\n\tv, err := n.occ.PutTag(bucket, key, name, value, pre)")]
M["p2h-get-flips-a-byte-sometimes"] = [(NODE_OBJ, "\t\tn.stats.Gets++\n\t\tcb(oi, res.Data, nil)", "\t\tn.stats.Gets++\n\t\tif len(res.Data) > 1 && n.stats.Gets%7 == 3 {\n\t\t\td := append([]byte(nil), res.Data...)\n\t\t\td[0] ^= 0x80\n\t\t\tres.Data = d\n\t\t}\n\t\tcb(oi, res.Data, nil)")]
M["p2i-startup-ignores-exited-node"] = []  # filled below

M["p2j-gc-collects-some-live-shards"] = [("internal/node/inventory.go", "\t\tif _, ok := exp[name]; ok || inv.rebuilding[name] {",
                                           "\t\tif _, ok := exp[name]; (ok && len(name)%3 != 0) || inv.rebuilding[name] {")]

# Planted failure used for the REPLAY check (Part 3): E7 fails at the first heal of the run.
M["p3-plant-e7-failure"] = [(NEM, "\t\td, err := c.Await(\"convergence after the heal\", nemHealBound, c.Converged)",
                             "\t\tif ci == 1 {\n\t\t\treturn failf(\"planted opus failure at cycle %d\", ci)\n\t\t}\n\t\td, err := c.Await(\"convergence after the heal\", nemHealBound, c.Converged)")]

def read(p):
    with open(os.path.join(ROOT, p)) as f:
        return f.read()

# fill the two that depend on code we only glance at
M["p2c-blocklist-skips-equal-size"] = [("internal/noderuntime/e2efault.go", "\tb.peers = peers\n\treturn nil\n}",
    "\tif b.peers != nil && len(b.peers) == len(peers) && len(peers) > 0 {\n\t\treturn nil\n\t}\n\tb.peers = peers\n\treturn nil\n}")]
M["p2i-startup-ignores-exited-node"] = [("test/e2e/realcluster_test.go", "\tfor _, p := range c.procs {\n\t\tif !p.running() {\n\t\t\tcode := -1",
    "\tfor _, p := range c.procs {\n\t\tif false && !p.running() {\n\t\t\tcode := -1")]

def main():
    os.makedirs(OUT, exist_ok=True)
    only = set(sys.argv[1:])
    bad = 0
    for name, edits in M.items():
        if only and name not in only:
            continue
        if not edits:
            print("SKIP %s (no edits defined)" % name); continue
        tmp = tempfile.mkdtemp()
        a, b = os.path.join(tmp, "a"), os.path.join(tmp, "b")
        files = sorted({p for p, _, _ in edits})
        for side in (a, b):
            for p in files:
                os.makedirs(os.path.dirname(os.path.join(side, p)), exist_ok=True)
                if os.path.exists(os.path.join(ROOT, p)):
                    shutil.copy(os.path.join(ROOT, p), os.path.join(side, p))
        ok = True
        for p, old, new in edits:
            fp = os.path.join(b, p)
            if old is None:
                with open(fp, "w") as f: f.write(new)
                continue
            s = open(fp).read()
            if s.count(old) != 1:
                print("FAIL %s: %s: old text found %d times" % (name, p, s.count(old))); ok = False; break
            with open(fp, "w") as f: f.write(s.replace(old, new))
        if ok:
            r = subprocess.run(["diff", "-ruN", "a", "b"], cwd=tmp, capture_output=True, text=True)
            with open(os.path.join(OUT, name + ".patch"), "w") as f: f.write(r.stdout)
            print("OK   %s (%d lines)" % (name, r.stdout.count("\n")))
        else:
            bad += 1
        shutil.rmtree(tmp)
    sys.exit(1 if bad else 0)

main()
