#!/usr/bin/env python3
"""Fixtures live only in temporary trees; tool output is retained beside this file."""
import json, os, shutil, subprocess, sys, tempfile, time, traceback
from pathlib import Path
def setup_error(kind, value, tb):
    traceback.print_exception(kind, value, tb)
    sys.exit(2)
sys.excepthook=setup_error
HERE=Path(__file__).resolve().parent
ROOT=HERE.parents[2]
GROUP=sys.argv[1]
LOG=HERE/'evidence'/GROUP
LOG.mkdir(parents=True,exist_ok=True)
results=[]

def run(args,cwd=None,env=None):
    p=subprocess.run(list(map(str,args)),cwd=cwd,env=dict(os.environ,**(env or {})),stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=180)
    return p.returncode,p.stdout

def put(root,path,text):
    p=root/path;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(text);return p

def record(claim,name,held,desc,out=''):
    line=f'{"HELD" if held else "BROKEN"} {claim} {name}: {desc}'
    print(line,flush=True);results.append(line)
    (LOG/(name+'.log')).write_text(out)

def must(args,cwd=None):
    rc,out=run(args,cwd)
    if rc: raise RuntimeError(f'{args}: exit {rc}\n{out}')
    return out

with tempfile.TemporaryDirectory(prefix='objstore-adversarial-') as tmp:
    T=Path(tmp); BIN=T/'bin'
    if GROUP not in ('site','prose'):
        must(['go','build','-o',str(BIN)+'/', './...'],ROOT/'agent_work/gates/detlint')
    def tree(name,full=False):
        p=T/name
        if full:
            shutil.copytree(ROOT,p,ignore=shutil.ignore_patterns('.git','kb','_site','adversarial','evidence'))
        else:
            p.mkdir();put(p,'go.mod','module example.org/probe\n\ngo 1.26.0\n')
        return p
    def reject(claim,name,args,desc,cwd=None):
        rc,out=run(args,cwd)
        if rc not in (0,1): raise RuntimeError(f'{name}: tool setup exit {rc}\n{out}')
        record(claim,name,rc==1,desc+f' (exit {rc}).',out)
    if GROUP=='determinism':
        cases={
          'alias-clock':'import clock "time"\nvar X = clock.Now',
          'dot-clock':'import . "time"\nvar X = Now',
          'struct-function':'import "time"\nvar X = struct{ F func() time.Time }{time.Now}',
          'map-function':'import "os"\nvar X = map[string]func()int{"pid":os.Getpid}',
          'embedded-sync':'import "sync"\ntype X struct {sync.Mutex}',
          'sync-map':'import "sync"\nvar X sync.Map',
          'reflect-pointer':'import "reflect"\nfunc X(p *int) uintptr {return reflect.ValueOf(p).Pointer()}',
          'reflect-mapkeys':'import "reflect"\nfunc X(m map[string]int) []reflect.Value {return reflect.ValueOf(m).MapKeys()}',
          'unsafe-address':'import "unsafe"\nfunc X(p *int) uintptr {return uintptr(unsafe.Pointer(p))}',
          'maphash':'import "hash/maphash"\nvar X = maphash.MakeSeed()',
          'percent-p':'import "fmt"\nfunc X(p *int) string {return fmt.Sprintf("%p",p)}',
          'percent-v-pointer':'import "fmt"\nfunc X(p *int) string {return fmt.Sprintf("%v",p)}',
          'percent-v-map-pointer-keys':'import "fmt"\nfunc X(m map[*int]int) string {return fmt.Sprintf("%v",m)}',
          'format-function-value':'import "fmt"\nfunc X(p *int) string {f:=fmt.Sprintf;return f("%p",p)}',
          'select':'func X(){select{default:}}',
          'getpid':'import "os"\nvar X = os.Getpid()',
          'maps-keys':'import "maps"\nfunc X(m map[string]int) string {for k:=range maps.Keys(m){return k};return ""}',
          'helper-map-range':'func helper(s string)string{return s}\nfunc X(m map[string]int)string {for k:=range m{return helper(k)};return ""}',
          'sort-ties':'import "sort"\nfunc X(m map[string]int)[]string {var a []string;for k:=range m{a=append(a,k)};sort.Slice(a,func(i,j int)bool{return false});return a}',
          'sortedfunc-ties':'import("maps";"slices")\nfunc X(m map[string]int)[]string{return slices.SortedFunc(maps.Keys(m),func(a,b string)int{return 0})}',
          'afterfunc':'import "time"\nfunc X(){time.AfterFunc(0,func(){})}',
          'context-afterfunc':'import "context"\nfunc X(c context.Context){context.AfterFunc(c,func(){})}',
          'context-timeout':'import("context";"time")\nfunc X(){context.WithTimeout(context.Background(),time.Second)}',
          'runtime-cpu':'import "runtime"\nvar X = runtime.NumCPU()',
          'runtime-memstats':'import "runtime"\nfunc X()uint64{var m runtime.MemStats;runtime.ReadMemStats(&m);return m.HeapAlloc}',
          'named-float':'type Real float64\nfunc X(a Real)Real{return a+1}',
          'generic-float':'func X[T ~float64](a T)T{return a+a}',
          'test-only-clock':'import "time"\nvar X=time.Now()',
          'tagged-clock':'import "time"\nvar X=time.Now()',
          'ignored-filename-clock':'import "time"\nvar X=time.Now()',
          'cgo-clock':'import "C"\nimport "time"\nvar X=time.Now()',
        }
        for name,body in cases.items():
            p=tree(name);fn='x.go'
            if name=='test-only-clock':fn='x_test.go'
            if name=='ignored-filename-clock':fn='_x.go'
            prefix='//go:build adversarial_unused\n\n' if name=='tagged-clock' else ''
            put(p,'internal/sim/'+fn,prefix+'package sim\n'+body+'\n')
            reject('C2',name,[BIN/'detlint','-root',p,'internal/sim'],body.replace('\n','; '),p)
        p=tree('helper-subpackage');put(p,'internal/env/x.go','package env\nimport "example.org/probe/internal/env/helper"\nvar X=helper.Now()\n');put(p,'internal/env/helper/x.go','package helper\nimport "time"\nfunc Now()int64{return time.Now().UnixNano()}\n')
        reject('C2','helper-subpackage',[BIN/'detlint','-root',p,'internal/env'],'Checked env imports helper.Now containing time.Now; helper is typechecked but not linted',p)
        # Safe control: fmt sorts ordinary scalar map keys; acceptance is not a bypass.
        p=tree('scalar-map-control');put(p,'internal/sim/x.go','package sim\nimport "fmt"\nfunc X(m map[string]int)string{return fmt.Sprintf("%v",m)}\n')
        rc,out=run([BIN/'detlint','-root',p,'internal/sim']);record('C2','scalar-map-control',rc==0,'Scalar-key map formatting accepted; fmt orders these keys, so this is a deterministic control.',out)
    elif GROUP=='hygiene':
        def gate(p,only='G0',extra=None):
            out=p.parent/(p.name+'-out')
            env={'GATE_ONLY':only,'GATE_OUT':str(out),'GATE_SHA':'a'*40,'GATE_DIRTY':'false','GATE_IMAGE_ID':'sha256:adversarial','FUZZ_SECONDS':'1'};env.update(extra or {})
            rc,log=run(['bash','agent_work/gates/gate.sh','adversarial'],p,env)
            result=json.loads((out/'result.json').read_text()) if (out/'result.json').exists() else None
            return rc,log,result
        p=tree('baseline',True);rc,log,result=gate(p)
        if rc or not result or result['overall']!='pass':raise RuntimeError('Baseline G0 is not green:\n'+log)
        cases={
          'unformatted':('C1','internal/buildcheck/attack.go','package buildcheck\nfunc   Attack( )int{return 1}\n'),
          'vet-format':('C1','internal/buildcheck/attack.go','package buildcheck\nimport "fmt"\nfunc Attack() {fmt.Printf("%d", "bad")}\n'),
          'build-error':('C1','internal/buildcheck/attack.go','package buildcheck\nvar Attack = undefined\n'),
          'direct-panic':('C1','internal/buildcheck/attack.go','package buildcheck\nfunc Attack(){panic("bad")}\n'),
          'rapid-product':('C1','internal/buildcheck/attack.go','package buildcheck\nimport "pgregory.net/rapid"\nvar Attack = rapid.Int()\n'),
          'comment':('C1','internal/buildcheck/attack.go','package buildcheck\n// ordinary prose\nvar Attack = 1\n'),
          'readme-machine':('C1','README.md','# Probe\n/root/secret\n'),
          'unknown-go-directive':('C3','internal/buildcheck/attack.go','package buildcheck\n//go:This is ordinary prose disguised as an unknown directive\nvar Attack = 1\n'),
          'block-comment':('C3','internal/buildcheck/attack.go','package buildcheck\n/* prose */\nvar Attack = 1\n'),
          'spaced-directive':('C3','internal/buildcheck/attack.go','package buildcheck\n// go:prose\nvar Attack = 1\n'),
          'hidden-comment':('C3','internal/buildcheck/_attack.go','package buildcheck\n// prose\n'),
          'dot-directory-comment':('C3','.hidden/x.go','package hidden\n// prose\n'),
          'underscore-directory-comment':('C3','_hidden/x.go','package hidden\n// prose\n'),
          'testdata-comment':('C3','internal/buildcheck/testdata/x.go','package fixture\n// prose\n'),
          'generated-header':('C3','internal/buildcheck/attack.go','// Code generated arbitrary human prose DO NOT EDIT.\npackage buildcheck\n'),
          'invalid-hidden-go':('C3','_broken.go','this is not Go\n'),
          'tagged-external-import':('C3','internal/buildcheck/attack.go','//go:build missingtag\n\npackage buildcheck\nimport _ "example.org/evil"\n'),
          'go-work':('C3','go.work','go 1.26.11\nuse .\n'),
          'vendor':('C3','vendor/modules.txt',''),
          'nested-module-require':('C3','extras/go.mod','module example.org/extra\ngo 1.26.0\nrequire example.org/evil v1.0.0\n'),
          'test-only-entropy':('C1','internal/sim/attack_test.go','package sim\nimport "time"\nvar Attack=time.Now()\n'),
          'tagged-entropy':('C1','internal/sim/attack.go','//go:build missingtag\n\npackage sim\nimport "time"\nvar Attack=time.Now()\n'),
        }
        for name,(claim,path,body) in cases.items():
            p=tree(name,True);file=put(p,path,body)
            if path.endswith('.go') and name not in ('unformatted','invalid-hidden-go'):must(['gofmt','-w',file])
            rc,log,res=gate(p)
            if not res:
                if rc not in (1,2):raise RuntimeError(name+' produced no result\n'+log)
                res={'overall':'aborted (exit '+str(rc)+')'}
            record(claim,name,res['overall']!='pass',f'Plant {path}: '+body.strip().replace('\n','; ')+f'; G0 {res["overall"]}.',log)
        for name,line in [('replace','replace pgregory.net/rapid => pgregory.net/rapid v1.2.0'),('tool','tool pgregory.net/rapid'),('extra-require','require example.org/evil v1.0.0')]:
            p=tree(name,True)
            with (p/'go.mod').open('a') as f:f.write('\n'+line+'\n')
            rc,log,res=gate(p)
            if not res:
                if rc not in (1,2):raise RuntimeError(name+log)
                res={'overall':'aborted (exit '+str(rc)+')'}
            record('C3',name,res['overall']!='pass',line+f'; G0 {res["overall"]}.',log)
        p=tree('panic-shadow-tag',True)
        put(p,'internal/buildcheck/attack.go','package buildcheck\nfunc Attack() {panic("unlisted")}\n')
        put(p,'internal/buildcheck/shadow.go','//go:build missingtag\n\npackage buildcheck\nfunc panic(any){}\n')
        must(['gofmt','-w',p/'internal/buildcheck'])
        rc,log,res=gate(p);record('C1','panic-shadow-tag',res['overall']!='pass','A build-excluded func panic(any) suppresses the active builtin panic in another file; G0 '+res['overall']+'.',log)
    elif GROUP=='astdiff':
        changes={
          'string-literal':('x.go','package p\nvar X="a"\n','package p\nvar X="b"\n'),
          'struct-tag':('x.go','package p\ntype X struct {A int `json:"a"`}\n','package p\ntype X struct {A int `json:"b"`}\n'),
          'build-tag':('x.go','//go:build linux\n\npackage p\n','//go:build darwin\n\npackage p\n'),
          'embed-directive':('x.go','package p\nimport _ "embed"\n//go:embed a.txt\nvar X string\n','package p\nimport _ "embed"\n//go:embed b.txt\nvar X string\n'),
          'embed-payload':('help.txt','old runtime help','new runtime help'),
          'module-change':('go.mod','module example.org/a\n','module example.org/b\n'),
          'assembly-change':('x.s','TEXT ·X(SB),$0-8\nMOVQ $1, ret+0(FP)\nRET\n','TEXT ·X(SB),$0-8\nMOVQ $2, ret+0(FP)\nRET\n'),
          'cgo-preamble':('x.go','package p\n/* int x() {return 1;} */\nimport "C"\nfunc X()int{return int(C.x())}\n','package p\n/* int x() {return 2;} */\nimport "C"\nfunc X()int{return int(C.x())}\n'),
        }
        for name,(path,a,b) in changes.items():
            p=tree(name+'a');q=tree(name+'b');put(p,'x.go','package p\nimport _ "embed"\n//go:embed help.txt\nvar X string\n');put(q,'x.go',(p/'x.go').read_text());put(p,path,a);put(q,path,b)
            for t in (p,q):
                if name!='embed-payload':put(t,'help.txt','runtime help\n')
                put(t,'a.txt','payload A\n');put(t,'b.txt','payload B\n')
                if name=='assembly-change':put(t,'x.go','package p\nfunc X() int64\n')
            reject('C4',name,[BIN/'astdiff',p,q],f'Changed {path} from {a.strip()!r} to {b.strip()!r}')
        p=tree('comment-a');q=tree('comment-b');put(p,'x.go','package p\nvar X=1\n');put(q,'x.go','package p\n// prose\nvar X=1\n')
        rc,out=run([BIN/'astdiff',p,q]);record('C4','comment-control',rc==0,'An ordinary comment-only edit is accepted.',out)
    elif GROUP=='site':
        def check(name,mutate,desc,leak=False):
            p=tree(name,True);mutate(p)
            rc,out=run(['python3',p/'agent_work/gates/site_lint.py',p])
            if rc not in (0,1):raise RuntimeError(name+out)
            if leak:
                dest=T/(name+'-site');br,bo=run(['python3',p/'.github/scripts/build_site.py','--root',p,dest]);out+='\n'+bo
                published=br==0 and any('PRIVATE_SENTINEL_72' in f.read_text(errors='replace') for f in dest.rglob('*') if f.is_file())
                record('C6',name,not published,desc+f'; lint exit {rc}, private sentinel published={published}.',out)
            else:record('C6',name,rc==1,desc+f'; lint exit {rc}.',out)
        def chapter(p,file='chapters/probe.md',text='# Probe\n',title='Probe'):
            put(p,'book/'+file,text)
            m=json.loads((p/'book/manifest.json').read_text());m['chapters'].append({'n':99,'slug':'probe','title':title,'file':file,'status':'draft'});put(p,'book/manifest.json',json.dumps(m))
        check('missing-chapter',lambda p:(chapter(p), (p/'book/chapters/probe.md').unlink()),'Manifest lists absent chapter')
        check('unlisted-chapter',lambda p:put(p,'book/chapters/unlisted.md','# Unlisted\n'),'Unlisted markdown chapter')
        check('broken-link',lambda p:chapter(p,text='# Probe\n[missing](nowhere.html)\n'),'Chapter links to nonexistent file')
        check('missing-anchor',lambda p:chapter(p,text='# Probe\n[missing](#nonexistent-anchor)\n'),'Chapter links to nonexistent heading')
        check('machine-reference',lambda p:chapter(p,text='# Probe\n/root/private\n'),'Chapter names a machine path')
        def traversal(p):
            chapter(p);m=json.loads((p/'book/manifest.json').read_text());m['chapters'][-1]['file']='../STATE.md';put(p,'book/manifest.json',json.dumps(m))
        check('manifest-traversal',traversal,'Manifest file ../STATE.md')
        for top in ['docs/knowledge/private.md','agent_work/private.md','kb/private.md','STATE.md']:
            def symlink(p,top=top):
                put(p,top,'# Probe\nPRIVATE_SENTINEL_72\n');chapter(p);(p/'book/chapters/probe.md').unlink();(p/'book/chapters/probe.md').symlink_to('../../'+top)
            check('chapter-symlink-'+top.split('/')[0],symlink,'Manifest chapter symlinks to '+top,True)
        def asset(p):
            put(p,'docs/knowledge/private.txt','PRIVATE_SENTINEL_72\n');chapter(p,text='# Probe\n[asset](../asset.txt)\n');(p/'book/asset.txt').symlink_to('../docs/knowledge/private.txt')
        check('asset-symlink',asset,'Linked book/asset.txt symlinks to private knowledge',True)
        def private_link(p):
            put(p,'docs/knowledge/private.md','# Private\nPRIVATE_SENTINEL_72\n');chapter(p,text='# Probe\n[private](../../docs/knowledge/private.md)\n')
        check('private-link',private_link,'Link to private knowledge is rewritten to GitHub (content sentinel must remain unpublished)',True)
    elif GROUP=='depth':
        # Confirm that lint misses survive all of G0, using real compilable programs.
        attacks={
          'g0-reflect-mapkeys': 'import "reflect"\nfunc X()[]reflect.Value{return reflect.ValueOf(map[int]int{1:1,2:2}).MapKeys()}',
          'g0-context-afterfunc':'import "context"\nfunc X(c context.Context){context.AfterFunc(c,func(){})}',
          'g0-pointer-value':'import "fmt"\nfunc X(p *int)string{return fmt.Sprintf("%v",p)}',
          'g0-clock-helper':'import "github.com/nickstrad/decentralized-object-store/internal/hiddenhelper"\nfunc X()int64{return hiddenhelper.Now()}',
          'g0-assembly-clock':'func X() uint64',
          'g0-comment-stage-typo':'// prose written during build\nvar X=1',
          'g0-linkname':'import _ "unsafe"\n//go:linkname X runtime.nanotime\nfunc X() int64',
        }
        for name,body in attacks.items():
            p=tree(name,True);put(p,'internal/sim/x.go','package sim\n'+body+'\n')
            if name=='g0-clock-helper':put(p,'internal/hiddenhelper/x.go','package hiddenhelper\nimport "time"\nfunc Now()int64{return time.Now().UnixNano()}\n')
            if name=='g0-assembly-clock':put(p,'internal/sim/x_amd64.s','#include "textflag.h"\nTEXT ·X(SB),NOSPLIT,$0-8\n RDTSC\n SHLQ $32, DX\n ORQ DX, AX\n MOVQ AX, ret+0(FP)\n RET\n')
            must(['gofmt','-w',p/'internal'])
            out=T/(name+'-out');env={'GATE_ONLY':'G0','GATE_OUT':str(out),'GATE_SHA':'a'*40,'GATE_DIRTY':'false','GATE_IMAGE_ID':'probe'}
            if name=='g0-comment-stage-typo':env['GATE_STAGE']='buidl'
            rc,log=run(['bash','agent_work/gates/gate.sh','probe'],p,env)
            # Orchestrator edit after the repair round: the gate now refuses an invalid stage
            # outright (exit 2, no result file). A refusal is a rejection, so it is HELD.
            if not (out/'result.json').exists():
                if rc==2:
                    record('C1',name,True,body.replace('\n','; ')+f'; gate refused to run (exit 2, no result file).',log);continue
                raise RuntimeError(log)
            res=json.loads((out/'result.json').read_text())
            desc=body.replace('\n','; ')
            if name=='g0-assembly-clock':
                desc='x_amd64.s implements func X() uint64 with RDTSC'
                put(p,'internal/sim/witness_test.go','package sim\nimport "testing"\nfunc TestCounter(t *testing.T){a,b:=X(),X();if a==b{t.Fatal("counter did not advance")};t.Log(a,b)}\n')
                witness=must(['go','test','-v','./internal/sim','-run','TestCounter'],p)
                log+='\nRUNTIME WITNESS\n'+witness
            if name=='g0-comment-stage-typo':desc='GATE_STAGE=buidl skips comment check; '+desc
            record('C1',name,res['overall']!='pass',desc+f'; full G0 {res["overall"]}.',log)
        # A native fuzz target explicitly selected away by an inherited environment variable.
        p=tree('fuzz-filter',True);put(p,'internal/buildcheck/x_test.go','package buildcheck\nimport "testing"\nfunc FuzzAttack(f *testing.F){f.Add("seed");f.Fuzz(func(t *testing.T,s string){t.Fatal("must run")})}\n')
        rc,log=run(['bash','agent_work/gates/fuzz-short.sh'],p,{'FUZZ_ONLY':'^Nothing$','FUZZ_SECONDS':'1'})
        record('C5','fuzz-filter',rc!=0,'FUZZ_ONLY=^Nothing$ filters an always-failing FuzzAttack out of the runner; exit '+str(rc)+'.',log)
        p=tree('no-packages',True);shutil.rmtree(p/'internal')
        rc,log=run(['bash','agent_work/gates/fuzz-short.sh'],p,{'FUZZ_SECONDS':'1'})
        record('C5','fuzz-no-packages',rc!=0,'Root module with no Go packages; fuzz runner exit '+str(rc)+'.',log)
    elif GROUP=='prose':
        # Host-only: prose-check itself invokes Docker. All git writes occur in /tmp fixtures.
        def fixture(name):
            p=tree(name,True)
            for x in ['.gitignore','internal','cmd','book','docs']:
                f=p/x
                if f.is_dir():shutil.rmtree(f)
                elif f.exists():f.unlink()
            put(p,'x.go','package p\nvar X=1\n');put(p,'README.md','# Probe\n')
            must(['git','init','-q','-b','main'],p);must(['git','config','user.email','probe@example.invalid'],p);must(['git','config','user.name','Adversarial fixture'],p)
            commit(p,'base');base=must(['git','rev-parse','HEAD'],p).strip();return p,base
        def commit(p,msg):
            must(['git','add','-A'],p);must(['git','commit','-qm',msg],p)
        for name in ['preexisting-comment','prose-code-change','post-readme','post-docs','post-comment-deletion','post-comment-duplication','embedded-help','merge-comment']:
            p,base=fixture(name)
            if name=='preexisting-comment':
                put(p,'x.go','package p\n// early\nvar X=1\n');commit(p,'builder comment')
            put(p,'x.go','package p\n// approved\nvar X=1\n');put(p,'docs/guide.md','# Approved\n');put(p,'cmd/probe/help.txt','old help\n')
            if name=='prose-code-change':put(p,'x.go','package p\n// approved\nvar X=2\n')
            commit(p,'prose\n\nProse-By: Gemini 4 Neon')
            if name=='post-readme':put(p,'README.md','# Unauthorized\n');commit(p,'later')
            if name=='post-docs':put(p,'docs/guide.md','# Unauthorized\n');commit(p,'later')
            if name=='post-comment-deletion':put(p,'x.go','package p\nvar X=1\n');commit(p,'later')
            if name=='post-comment-duplication':put(p,'x.go','package p\n// approved\nvar X=1\n// approved\nvar Y=2\n');commit(p,'later')
            if name=='embedded-help':
                # The prose commit permits help.txt edits; this file is a runtime embed input.
                put(p,'x.go','package p\nimport _ "embed"\n//go:embed cmd/probe/help.txt\nvar X string\n');commit(p,'builder adds embedding')
                put(p,'cmd/probe/help.txt','changed embedded behavior\n');commit(p,'prose\n\nProse-By: Gemini 4 Neon')
            if name=='merge-comment':
                must(['git','checkout','-qb','side'],p);put(p,'side.txt','side');commit(p,'side');must(['git','checkout','-q','main'],p);put(p,'main.txt','main');commit(p,'main');must(['git','merge','--no-commit','--no-ff','side'],p);put(p,'x.go','package p\n// unauthorized merge comment\nvar X=1\n');commit(p,'merge')
            rc,out=run(['bash','agent_work/gates/prose-check.sh',base,'HEAD'],p)
            if rc not in (0,1):raise RuntimeError(name+out)
            record('C4',name,rc==1,'Throwaway commit range tests '+name+f'; prose-check exit {rc}.',out)
    elif GROUP=='provenance':
        def gate(p,name,only='',extra=None):
            out=T/(name+'-out');env={'GATE_ONLY':only,'GATE_OUT':str(out),'GATE_SHA':'not-a-commit','GATE_DIRTY':'false','GATE_IMAGE_ID':'invented-image','FUZZ_SECONDS':'1'};env.update(extra or {})
            rc,log=run(['bash','agent_work/gates/gate.sh','adversarial'],p,env)
            f=out/'result.json';res=json.loads(f.read_text()) if f.exists() and f.stat().st_size else None
            return rc,log,res,out
        p=tree('forged',True)
        rc,log,res,out=gate(p,'forged')
        if not res:raise RuntimeError(log)
        record('C5','forged-provenance',not res['counts_for_landing'],'Unversioned copy with GATE_SHA=not-a-commit GATE_DIRTY=false GATE_IMAGE_ID=invented-image; counts_for_landing='+str(res['counts_for_landing'])+'.',log)
        for name,only,extra in [('partial','G0',{}),('invalid-gate','G99',{}),('dirty','',{'GATE_DIRTY':'true'}),('unknown-sha','',{'GATE_SHA':'unknown'})]:
            rc,log,res,out=gate(p,name,only,extra)
            if not res:raise RuntimeError(log)
            record('C5',name,not res['counts_for_landing'],f'GATE_ONLY={only!r}, overrides={extra}; counts_for_landing={res["counts_for_landing"]}.',log)
        # A failing property exists, imports rapid, but has no Laws/Prop substring.
        q=tree('unmatched-property',True);put(q,'internal/buildcheck/missed_test.go','package buildcheck\nimport("testing";"pgregory.net/rapid")\nfunc TestInvariant(t *testing.T){rapid.Check(t,func(t *rapid.T){t.Fatal("must run")})}\n')
        direct,dlog=run(['go','test','./internal/buildcheck','-run','TestInvariant'],q)
        if direct!=1:raise RuntimeError('Property control did not fail: '+dlog)
        rc,log,res,out=gate(q,'unmatched-property','G2');log+='\nDIRECT CONTROL\n'+dlog;record('C5','unmatched-property',res['overall']!='pass','TestInvariant always fails but G2 -run Laws|Prop selects zero tests; G2 '+res['overall']+'.',log)
        # Go accepts Unicode uppercase fuzz names; the ASCII scanner does not.
        q=tree('unicode-fuzz',True);put(q,'internal/buildcheck/missed_test.go','package buildcheck\nimport "testing"\nfunc FuzzÉ(f *testing.F){f.Add("x");f.Fuzz(func(t *testing.T,s string){t.Fatal("must run")})}\n')
        listing=must(['go','test','./internal/buildcheck','-list','Fuzz'],q)
        if 'FuzzÉ' not in listing:raise RuntimeError('Go did not discover Unicode fuzz target')
        direct,dlog=run(['go','test','./internal/buildcheck','-run','FuzzÉ'],q)
        if direct!=1:raise RuntimeError('Fuzz control did not fail: '+dlog)
        rc,log,res,out=gate(q,'unicode-fuzz','G3');log+='\nDIRECT CONTROL\n'+dlog;record('C5','unicode-fuzz',res['overall']!='pass','go test discovers FuzzÉ but ASCII scan runs zero targets; G3 '+res['overall']+'.',listing+log)
        q=tree('indirect-dst',True)
        put(q,'internal/sim/x.go','package sim\nfunc X(){}\n');put(q,'internal/bridge/x.go','package bridge\nimport "github.com/nickstrad/decentralized-object-store/internal/sim"\nfunc X(){sim.X()}\n');put(q,'internal/consumer/x_test.go','package consumer\nimport("os";"testing";"github.com/nickstrad/decentralized-object-store/internal/bridge")\nfunc TestScenario(t *testing.T){bridge.X();if os.Getenv("DST_SEEDS")=="500"{t.Fatal("must run")}}\n')
        direct,dlog=run(['go','test','./internal/consumer'],q,{'DST_SEEDS':'500'})
        if direct!=1:raise RuntimeError('DST control did not fail: '+dlog)
        rc,log,res,out=gate(q,'indirect-dst','G5');log+='\nDIRECT CONTROL\n'+dlog;record('C5','indirect-dst',res['overall']!='pass','Scenario imports bridge which imports sim; direct-import package scan skips consumer; G5 '+res['overall']+'.',log)
        q=tree('coverage-no-tests',True);put(q,'internal/sim/x.go','package sim\nfunc X()int{return 1}\n')
        rc,log,res,out=gate(q,'coverage-no-tests','G1');record('C5','coverage-no-tests',res['overall']!='pass','Uncovered simulator package has no tests; G1 '+res['overall']+'.',log)
        # Force failure before result replacement, leaving previously valid output in place.
        q=tree('stale-output',True);dest=T/'stale-output-out';dest.mkdir();put(dest,'result.json',json.dumps({'counts_for_landing':True,'overall':'pass','sha':'earlier'}))
        rc,log=run(['bash','agent_work/gates/gate.sh'],q,{'GATE_OUT':str(dest)})
        stale=json.loads((dest/'result.json').read_text())['counts_for_landing']
        record('C5','stale-usage-failure',not stale,'A prior result remains counts_for_landing=true after gate exits 2 for missing chunk; consumers must bind result to invocation.',log)
        # Failure after gate-side startup must remove the earlier result.
        put(q,'go.mod','invalid module syntax\n');rc,log,res,out=gate(q,'stale-output','G0')
        record('C5','stale-module-failure',not res or not res.get('counts_for_landing',False),'Invalid go.mod aborts after result cleanup; stale landing result must be gone.',log)
    else:raise RuntimeError('unknown group '+GROUP)
(LOG/'lines.txt').write_text('\n'.join(results)+'\n')
sys.exit(1 if any(x.startswith('BROKEN') for x in results) else 0)
