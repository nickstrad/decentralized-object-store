import json, os, pathlib, subprocess, sys
base=pathlib.Path('agent_work/adversarial/03-merkle')
names={
'C1':['TestRootKnownVectors'], 'C2':['TestInsertDeleteUpdate','TestKeysAndEntriesSorted','TestHashString'],
'C3':['TestEmptyAndSingle'], 'C4':['TestNewAndDepthFor'], 'C5':['TestKeyLimits','TestLeafFull'],
'C6':['TestSnapshotIsolation'], 'C7':['TestDiffProtocolSteps'], 'C8':['TestDiffLeafRepliesAreBatched'],
'C9':['TestDiffFrontierTruncates'], 'C10':['TestWireTable','TestWireMalformed'],
'C11':['TestPropDiffComplete'], 'C12':['TestPropDiffCost'], 'C13':['TestPropRootIffEqual'],
'C14':['TestPropIncrementalEqualsRebuild'], 'C15':['TestPropOrderIndependent'],
'C16':['TestPropHostileRepliesEndTheSession'], 'C17':['FuzzMerkleWire','FuzzMerkleDiff'],
'C18':['TestBitRotRepairDST'], 'C19':['TestMissedWriteRepairDST'], 'C20':['TestRepairUnderLossDST'],
'C21':['TestLyingPeerDST'], 'C22':['TestScrubRefillsAWipedDiskDST'],
'C23':['TestErrorPeerVanishesMidDiff'], 'C24':['TestErrorMalformedNode','TestDiffErrors'],
'C25':['TestErrorDepthOverflow'], 'C26':['TestCodeEntries'], 'C27':['TestNewWorkerValidation'],
'C28':['TestCanarySkippedBranch']}
pattern='^('+'|'.join(n for ns in names.values() for n in ns)+')$'
cmd=['docker','compose','run','--rm','-T','dev','env','-u','DST_SEED','DST_SEEDS=12','DST_SEED_BASE=731903',
'DST_ARTIFACTS=/src/'+str(base/'evidence'/'dst'),'go','test','-json','-timeout=5m','-count=1','./internal/merkle',
'-run',pattern,'-rapid.checks=250','-rapid.seed=731903']
with (base/'evidence/regression.jsonl').open('w') as out, (base/'evidence/regression.stderr').open('w') as err:
    result=subprocess.run(cmd,stdout=out,stderr=err)
statuses={}
for line in (base/'evidence/regression.jsonl').read_text().splitlines():
    try: row=json.loads(line)
    except ValueError: continue
    if row.get('Test') and '/' not in row['Test'] and row.get('Action') in ('pass','fail','skip'):
        statuses[row['Test']]=row['Action']
setup=broken=0
for c,ns in names.items():
    for name in ns:
        status=statuses.get(name)
        if status=='pass': print(f'HELD {c} regression-{name}: existing test passed with DST base 731903, 12 seeds and 250 rapid checks where applicable')
        elif status=='fail':
            broken+=1
            print(f'BROKEN {c} regression-{name}: seed base=731903; run a04-regression.sh and inspect evidence/regression.jsonl for the failing subcase')
        else:
            setup+=1
            print(f'SETUP {c} regression-{name}: expected test did not complete; status={status}')
if result.returncode and not broken and not setup:
    setup+=1; print('SETUP regression: package command failed without an attributed test failure')
sys.exit(2 if setup else 1 if broken else 0)
