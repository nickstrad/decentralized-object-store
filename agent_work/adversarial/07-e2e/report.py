import json,re,sys
from pathlib import Path
p=Path(sys.argv[1]); rc=int(sys.argv[2]); events=[]
for line in p.read_text().splitlines():
 try: events.append(json.loads(line))
 except ValueError: pass
names={e['Test'] for e in events if e.get('Action')=='run' and 'Test' in e}
leaves=sorted(n for n in names if not any(m.startswith(n+'/') for m in names))
setup=False; broken=False
for n in leaves:
 es=[e for e in events if e.get('Test')==n]
 log=''.join(e.get('Output','') for e in es)
 actions=[e.get('Action') for e in es]
 claim=re.search(r'/(C\d+)(?:/|$)',n)
 if not claim: continue
 claim=claim[1]
 if 'skip' in actions or 'SETUP:' in log or not any(a in actions for a in ('pass','fail')):
  setup=True;print('SETUP '+n+': check did not complete.',file=sys.stderr);continue
 lines=[l.strip() for l in log.splitlines() if '.go:' in l]
 detail=lines[-1] if lines else 'completed all assertions'
 if 'fail' in actions:
  broken=True
  detail=next((l for l in lines if not any(s in l for s in ['MEASURE','log tail','objects=','rotted=','filled='])),detail)
 detail=' '.join(detail.split())[:900].rstrip('.')+'.'
 print(('BROKEN' if 'fail' in actions else 'HELD')+' '+claim+' '+n.split('/',1)[-1]+': '+detail)
if not leaves or (rc and not broken): setup=True
if setup: print('SETUP: see '+str(p)+' and companion stderr',file=sys.stderr)
sys.exit(2 if setup else 1 if broken else 0)
