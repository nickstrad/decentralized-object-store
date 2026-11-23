from pathlib import Path
import csv,json,re
out=Path(__file__).parent
root=out.parents[2]
def line(path,needle):
 for i,l in enumerate((root/path).read_text().splitlines(),1):
  if needle in l:return i
 raise ValueError((path,needle))
def ref(path,needle):return f'{path}:{line(path,needle)}'
B=[]
def finding(sev,chapter,needle,kind,evidence,fix):
 path=next((root/'book/chapters').glob(chapter+'-*.md'))
 B.append(dict(id=f'B{len(B)+1:02}',severity=sev,source=str(path.relative_to(root)),line=line(str(path.relative_to(root)),needle),sentence=needle,kind=kind,evidence=evidence,fix=fix))
finding('MINOR','01','Virtual time advances only when the simulator pops the next event from its queue.','contradicted',ref('internal/sim/sim.go','func (s *Sim) RunUntil(t')+' advances now to the requested deadline even with an empty queue (line 611).','Virtual time advances to the next event or an explicitly requested run deadline.')
finding('MINOR','01','writing a full log when `DST_ARTIFACTS` is set','stronger than evidence',ref('internal/sim/log.go','func (s *Sim) WriteLog')+' writes retained ring entries and a truncation header; LogCap defaults to 2^20.','writing the retained event log, with a truncation marker if necessary, when DST_ARTIFACTS is set')
finding('MINOR','02','across four memory-bounded shards','contradicted',ref('internal/gossip/dst_test.go','func shardedSweep')+' is called only by TestSWIMConvergenceDST at line 454; the other suites call Sweep directly.','The convergence suite uses four memory-bounded shards; the other scenarios sweep seeds sequentially within each scenario.')
finding('MINOR','02','`FuzzGossipWire` (30 s, 64,508 execs)','unsupported',ref('agent_work/01-gossip/build-notes.md','G3  fuzz')+' records 30-second runs but no execution count; agent_work/01-gossip/gates/result.json records only aggregate duration/target count. Exact count absent from tracked evidence (searched repository excluding book).','FuzzGossipWire ran for 30 s with no crashers (or supply the missing execution-count log).')
finding('MINOR','02','`FuzzViewMergeLaws` (30 s, 25,371 execs','unsupported',ref('agent_work/01-gossip/build-notes.md','G3  fuzz')+' and agent_work/01-gossip/gates/result.json support the run, not 25,371 executions. The only matching digit sequences elsewhere are unrelated timestamps/metrics.','FuzzViewMergeLaws ran for 30 s with no crashers (or supply the missing execution-count log).')
finding('BLOCKER','03','keeps every square submatrix invertible for `k + m <= 256`','contradicted',ref('internal/erasure/matrix.go','// CodingMatrix builds')+' guarantees k-by-k submatrices only. The identity block has singular 1-by-1 off-diagonal submatrices; claims_test.go reproduces this.','keeps every k-by-k submatrix of G invertible for k + m <= 256')
finding('MINOR','03','`AckQuorum` (default `k + 1`, within `[k, k + m]`)','contradicted',ref('internal/storage/node.go','func (c Config) withDefaults')+' uses min(K+1,K+M). The chapter explicitly allows m=0; its stated default would then be outside its own valid range.','AckQuorum (default min(k + 1, k + m), within [k, k + m])')
finding('BLOCKER','03','permanent damage (bit-rot, wipe) never makes a GET unavailable','stronger than evidence',ref('internal/storage/node.go','func (op *putOp) reply')+' acknowledges at quorum, not all n shards. '+ref('internal/storage/dst_test.go','dstChaosStart')+' starts faults at 10 s after fault-free PUTs at 1 s. The separate claims_test.go counterexample has five durable shards after a successful 4+2 PUT, heals the network, then rots two shards; Budget.Check passes but GET is unavailable without ErrIO. Chapter 7 explicitly recognizes unwritten shards can drop a stripe below k.','In this suite, after fault-free writes have settled, permanent damage within MaxDamagedPerGroup=m never made a GET unavailable. A quorum-only PUT does not yet guarantee m further losses can be tolerated.')
finding('MINOR','04','Equal roots stop in one message','contradicted',ref('internal/merkle/diff.go','func (s *Session)')+' implements a request/reply exchange; the chapter later correctly states 35-byte request plus 1-byte reply (one round trip, 36 bytes).','Equal roots stop in one round trip.')
finding('MINOR','04','Four 3-node suites','contradicted',ref('internal/merkle/dst_test.go','func lyingRun')+' creates four nodes (three honest plus one liar); loneLiarRun creates two.','The first three suites use three nodes; TestLyingPeerDST uses three honest nodes plus a liar, and a separate two-node lone-liar case.')
finding('MINOR','07','plus an 8-line `Node.Merge`','stale',ref('internal/occ/node_merge.go','func (n *Node) Merge')+' is six lines including declaration and closing brace (lines 7–12).','plus a small Node.Merge wrapper')
finding('MAJOR','08','Every Tier S failure prints a replay line:','stronger than evidence',ref('test/e2e/e7_test.go','t.Cleanup(func()')+' has parent-level aggregate sweep failures after child cleanup, without Report; '+ref('internal/sim/simtest/determinism.go','func RequireDeterministic')+' registers reports on the individual seed test. Single-seed replay also cannot reproduce a sweep floor requiring >=10/20 seeds. README:71 repeats the broader guarantee.','Failures in individual RequireDeterministic scenarios print a single-seed replay line; aggregate sweep checks require rerunning the original sweep.')
finding('MAJOR','03','`FuzzErasureCoding`: 18,227 up to `k = 256`, 3 MiB','contradicted',ref('internal/erasure/codec_test.go','func propErasureCoding')+' calls drawPayload(t,k,true); '+ref('internal/erasure/codec_test.go','func drawPayload')+' clamps small payloads to 64 KiB. Build-notes line 521 repeats the error, but code and the fuzz target comment agree on 64 KiB. Multi-MB coverage is in TestPropRoundTrip/TestRoundTripMultiMB, not this fuzzer.','FuzzErasureCoding: 18,227 executions with k up to 256 and payloads up to 64 KiB; separate round-trip tests cover multi-MB payloads.')
(out/'book-findings.json').write_text(json.dumps(B,indent=2))
# Keep whole prose lines as claim groups so every compound numeric claim is visible.
claims=json.loads((out/'claim-inventory.json').read_text())
claims=[c for c in claims if not re.fullmatch(r'[|: —\-]+',c['claim']) and not c['claim'].startswith(('| Topic |','|---','| N |','| Scenario (','| Scenario |','| # |','| *N* |'))]
chunks=['0b-simulator','01-gossip','02-erasure','03-merkle','04-occ','05-saga','06-integration','07-e2e']
tests=json.loads((out/'named-tests.json').read_text())
lines=['# Claim inventory\n','Count unit: one prose paragraph, bullet, or table row (a compound group); each quoted group retains all its numbers and qualifiers. Diagram labels and source excerpts are checked separately in mechanical-audit.json. Introductory illustrative examples are included for completeness, not counted as measured experiments. “No discrepancy found” is a repository consistency result, not a mathematical proof or a rerun of historical measurements.\n','| ID | Source | Claim group | Evidence / disposition |','|---|---|---|---|']
def esc(s):return s.replace('|','\\|').replace('\n',' ')
for i,c in enumerate(claims,1):
 c['id']=f'C{i:03d}'
 fs=[b for b in B if b['source']==c['source'] and b['line']==c['line']]
 refs=[]
 for n in re.findall(r'\b(?:Test|Fuzz|Benchmark)[A-Z][A-Za-z0-9_]*',c['claim']):
  for d in tests.get(n,{}).get('definitions',[]):refs.append(d['file']+':'+str(d['line']))
 for target in re.findall(r'\]\(([^)]+)\)',c['claim']):
  if '://' not in target:refs.append(str((root/c['source']).parent.joinpath(target).resolve().relative_to(root)))
 if 'chapters/' in c['source']:
  ch=int(Path(c['source']).name[:2])-1
  chunk=chunks[ch]
  notes=root/'agent_work'/chunk/'build-notes.md'
  text=notes.read_text()
  nums=[n for n in re.findall(r'(?<!\w)\d[\d,]*(?:\.\d+)?',c['claim']) if len(n)>2]
  for n in nums:
   needle=n.replace(',','')
   for j,l in enumerate(text.splitlines(),1):
    if needle in l.replace(',','').replace('_',''):
     refs.append(str(notes.relative_to(root))+':'+str(j));break
  if not refs:refs.append('agent_work/'+chunk+'/build-notes.md (implementation/claims sections); source files identified in this chapter')
 c['evidence']=list(dict.fromkeys(refs))
 c['finding_ids']=[b['id'] for b in fs]
 disposition=('Discrepancy: '+', '.join(c['finding_ids'])) if fs else 'No discrepancy found in the described scope'
 lines.append(f"| {c['id']} | {c['source']}:{c['line']} | {esc(c['claim'])} | {esc('; '.join(c['evidence']))}; {disposition} |")
(out/'CLAIMS.md').write_text('\n'.join(lines)+'\n')
(out/'claim-inventory.json').write_text(json.dumps(claims,indent=2))
print(len(claims),'claim groups;',len(B),'findings')
