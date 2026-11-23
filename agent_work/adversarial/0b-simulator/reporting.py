import os, pathlib, re, shlex, shutil, subprocess, tempfile, sys
base=pathlib.Path.cwd()
evidence=base/'evidence'
broken=0

def run(args,name,cwd=base):
    p=subprocess.run(args,cwd=cwd,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=90)
    (evidence/name).write_text(p.stdout)
    return p

def result(claim,name,ok,detail):
    global broken
    print(f'{"HELD" if ok else "BROKEN"} {claim} {name}: {detail}',flush=True)
    broken+=not ok

def digests(text):
    return re.findall(r'(?:events|state)=[a-f0-9]{64}',text)

p=run(['go','test','./reportfixture','-run','^TestFailure$','-seed=23','-count=1','-v'],'simtest-original.txt')
if 'simulation failed' not in p.stdout:
    raise RuntimeError('fixture did not reach simtest reporting: '+p.stdout)
parts=all(x in p.stdout for x in ['seed: 23','event log tail (5 of 10):','REPLAY:','panic stack:']) and len(digests(p.stdout))>=2
result('C7','simtest-report-fields',p.returncode==1 and parts,'seed=23 deliberate panic report checked for seed, both digests, truncated tail, stack and replay command')
cmd=re.search(r'REPLAY: ([^\n]+)',p.stdout)
if cmd:
    q=run(shlex.split(cmd[1].strip()),'simtest-replayed.txt')
    ok=q.returncode==p.returncode and digests(q.stdout)==digests(p.stdout) and 'replay-fixture' in q.stdout
    result('C7','simtest-exact-replay',ok,'seed=23 executed the printed command verbatim from the module root and compared failure and both digests')
else:
    result('C7','simtest-exact-replay',False,'seed=23 failure emitted no REPLAY command')

# A temporary copy of the production CLI is necessary because its registered scenarios
# only succeed. Only scenarios.go changes; the actual CLI runner is byte-for-byte copied.
with tempfile.TemporaryDirectory(prefix='.cli-',dir=base) as d:
    dst=pathlib.Path(d)
    root=base.parents[2]
    for name in ['main.go','help.txt']:
        shutil.copyfile(root/'cmd/objsim'/name,dst/name)
    (dst/'scenarios.go').write_text('''package main
import "github.com/nickstrad/object-store/internal/sim"
func scenarios() []sim.Scenario {return []sim.Scenario{{Name:"adversarial-fail",Run:func(seed int64)*sim.Sim{
s:=sim.New(sim.Config{Seed:seed});s.At(0,"deliberate",func(){panic("cli-replay-fixture")});_=s.RunUntilQuiet();return s
}}}}
''')
    build=run(['go','build','-o',str(dst/'objsim'),'./'+dst.name],'cli-build.txt')
    if build.returncode: raise RuntimeError(build.stdout)
    args=[str(dst/'objsim'),'-scenario','adversarial-fail','-seed','23']
    p=run(args,'cli-original.txt')
    q=run(args,'cli-repeated.txt')
    result('C7','cli-failure-digests',p.returncode==q.returncode==1 and len(digests(p.stdout))==2 and digests(p.stdout)==digests(q.stdout),'seed=23 copied production CLI with a registered panic scenario exited 1 twice with matching digests')
    tail=bool(re.search(r't=\d+ seq=\d+',p.stdout))
    replay=re.search(r'REPLAY: ([^\n]+)',p.stdout)
    ok=tail and bool(replay)
    if replay:
        q=run(shlex.split(replay[1]),'cli-replayed.txt')
        ok=ok and q.returncode==1 and digests(q.stdout)==digests(p.stdout)
    result('C7','cli-failure-report',ok,f'seed=23 production run(-scenario adversarial-fail -seed 23) emitted log-tail={tail} REPLAY={bool(replay)}; see cli-original.txt and reporting.py fixture')
sys.exit(1 if broken else 0)
