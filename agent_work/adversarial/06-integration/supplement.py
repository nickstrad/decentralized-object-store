import json, os, subprocess, sys
from pathlib import Path
p=Path('agent_work/adversarial/06-integration')
groups={
'C1':'TestJoinedCodeTableHasNoCollision|TestEveryCodeRoundTripsToItsSentinel|TestNodeAndAPICodesStayInTheirBlock',
'C2':'TestClusterPutGetDST', 'C3,C4,C5':'TestClusterPartitionHealDST',
'C6,C7':'TestClusterCrashRestartDST', 'C8':'TestClusterBitRotDST',
'C9':'TestClusterMigrationFailurePointsDST',
'C10':'TestBucketMigratingAwayStopsItsWorkers|TestNodeRefusesMalformedAndPostStopTraffic',
'C13,C24':'TestConformanceOnTheSimulator|TestConformanceOnLoopback|TestLoopbackClusterServesObjectsAcrossNodes',
'C14':'TestFrameRoundTrips|TestFrameDecodersRejectMalformedInput',
'C15':'TestFrameLimitCoversMerkleRepliesAndShardRequests|TestConfigValidationRejectsEachBadField|TestRuntimeConfigValidation',
'C16':'TestStatusRowsThroughHTTPOnARealNode|TestStatusTableRowByRowThroughHTTP',
'C17':'TestHTTPObjectAPIAgainstTheModel',
'C18':'TestServerBoundsHeadersBodiesAndSlowClients',
'C19':'TestRuntimeLifecycleInABubble|TestRuntimeCloseDuringStartup|TestRuntimeStartHonoursItsContext|TestRuntimeCallsBeforeStartAreNotReady',
'C20':'TestRuntimeCancelMidPutLeavesOnlyCollectableShards|TestCancelMidGetAndMidMigrationReturnContextErrors',
'C21':'TestShutdownUnderLoadReturnsEveryCall|TestShutdownDuringRepairAndRestartOnTheSameDisk',
'C22':'TestRealRuntimeLeavesNoGoroutineBehind|TestServeAndShutdownOnCancel',
'C23':'TestRuntimeRecoversAPanicOnTheLoop|TestRuntimeDatagramsToSelfAndQueueOverflow',
'C24':'TestParseFlagsAndConfigFile|TestInvalidConfigurationsExitTwo|TestHelpListsEveryFlag',
}
names={n:c for c,ns in groups.items() for n in ns.split('|')}
pattern='^('+'|'.join(names)+')$'
cmd=['docker','compose','run','--rm','-T','dev','env','DST_SEEDS=2','DST_SEED_BASE=6281','DST_ARTIFACTS=/src/'+str(p/'evidence/dst'),'GOMAXPROCS=4','go','test','-json','-race','-count=1','-parallel=4','-timeout=12m','-run',pattern,'./internal/node','./internal/storage','./cmd/objnode']
log=p/'evidence/supplement.jsonl'
with log.open('w') as out:r=subprocess.run(cmd,stdout=out,stderr=subprocess.STDOUT)
results={}; packages_failed=[]
for line in log.read_text().splitlines():
 try: row=json.loads(line)
 except ValueError: continue
 name=row.get('Test','');act=row.get('Action')
 if name in names and act in ('pass','fail','skip'):results[name]=act
 if not name and act=='fail':packages_failed.append(row.get('Package'))
broken=0;setup=0
for name,claim in names.items():
 if results.get(name)=='pass':print(f'HELD {claim} existing-{name}: existing suite passed with race detector, DST_SEED_BASE=6281 DST_SEEDS=2')
 elif results.get(name)=='fail':
  broken+=1;print(f'BROKEN {claim} existing-{name}: seed_base=6281 existing test failed; see evidence/supplement.jsonl')
 else:setup+=1;print(f'setup error: no passing/failing result for {name}',file=sys.stderr)
if packages_failed and not broken:setup+=1;print('setup error: package failure outside a reported test: '+str(packages_failed),file=sys.stderr)
if r.returncode and not (broken or setup):setup+=1
sys.exit(2 if setup else 1 if broken else 0)
