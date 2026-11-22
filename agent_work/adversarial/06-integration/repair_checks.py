import json,subprocess,sys
from pathlib import Path
p=Path('agent_work/adversarial/06-integration')
source='/src/'+str(p/'disk_overlay.txt')
(p/'disk-overlay.json').write_text(json.dumps({'Replace':{'/src/internal/noderuntime/independent_disk_test.go':source}}))
names={'TestIndependentDiskCrashBoundaries':'C32','TestFileDiskCrashLeavesOldOrNewBytes':'C32', 'TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST':'C29', 'TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST':'C33'}
cmd=['docker','compose','run','--rm','-T','dev','env','GOMAXPROCS=2','GOFLAGS=-p=2','DST_SEEDS=20','DST_SEED_BASE=7401','DST_ARTIFACTS=/src/'+str(p/'evidence/repair-dst'),'go','test','-json','-count=1','-parallel=2','-timeout=8m','-overlay=/src/'+str(p/'disk-overlay.json'),'-run','^('+'|'.join(names)+')$','./internal/node','./internal/noderuntime']
with (p/'evidence/repair.jsonl').open('w') as f:r=subprocess.run(cmd,stdout=f,stderr=subprocess.STDOUT)
results={}
for line in (p/'evidence/repair.jsonl').read_text().splitlines():
 try:x=json.loads(line)
 except ValueError:continue
 n=x.get('Test');a=x.get('Action')
 if n in names and a in ('pass','fail'):results[n]=a
 if 'HELD C32 disk-boundary:' in x.get('Output',''):print(x['Output'][x['Output'].index('HELD C32 disk-boundary:'):].strip())
bad=setup=0
for n,c in names.items():
 if results.get(n)=='pass':print(f'HELD {c} supplemental-{n}: passed; DST seeds 7401..7420, disk exhaustive operation boundaries; evidence/repair.jsonl')
 elif results.get(n)=='fail':bad+=1;print(f'BROKEN {c} supplemental-{n}: see evidence/repair.jsonl; DST base 7401')
 else:setup+=1;print(f'setup error: missing {n}',file=sys.stderr)
if r.returncode and not bad:setup+=1
sys.exit(2 if setup else 1 if bad else 0)
