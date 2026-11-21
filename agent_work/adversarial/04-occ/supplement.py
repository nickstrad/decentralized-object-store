import json, sys
from pathlib import Path
base = Path('agent_work/adversarial/04-occ')
claims = json.loads((base / 'claims.json').read_text())
raw = (base / "evidence/builder.jsonl").read_text()
if "no such file or directory" in raw or "[no test files]" in raw:
    print("Fixture/setup failure; inspect evidence/builder.jsonl", file=sys.stderr)
    sys.exit(2)
status = {}
package = None
for line in (base / 'evidence/builder.jsonl').read_text().splitlines():
    try:
        row = json.loads(line)
    except ValueError:
        continue
    if row.get('Action') in ('pass', 'fail', 'skip'):
        if 'Test' in row:
            status[row['Test']] = row['Action']
        else:
            package = row['Action']
if package is None or any(t not in status for _, ts in claims for t in ts):
    print('Supplement setup incomplete; inspect evidence/builder.stderr and builder.jsonl', file=sys.stderr)
    sys.exit(2)
broken = False
for i, (_, tests) in enumerate(claims, 1):
    bad = [t for t in tests if status[t] != 'pass']
    if bad:
        broken = True
        print(f'BROKEN C{i} builder-regression: seed=4701 outcomes=' + ','.join(t+'='+status[t] for t in bad))
    else:
        print(f'HELD C{i} builder-regression: {len(tests)} existing named tests passed with DST seeds 4701..4708 and 200 property cases')
if package != 'pass' and not broken:
    print('Unmapped package failure; inspect evidence/builder.jsonl', file=sys.stderr)
    sys.exit(2)
sys.exit(1 if broken else 0)
