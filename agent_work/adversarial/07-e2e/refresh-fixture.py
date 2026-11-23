"""Refresh reusable test support so a product repair is tested on rerun."""
from pathlib import Path
import hashlib,sys
base=Path('agent_work/adversarial/07-e2e')
lines=[]
for source in sorted(Path('test/e2e').glob('*.go')):
    data=source.read_bytes()
    lines.append(hashlib.sha256(data).hexdigest()+'  '+str(source)+'\n')
    text=data.decode()
    if source.name=='realcluster_test.go':
        old='filepath.Join(dir, "go.mod")'
        if old not in text:
            raise SystemExit('SETUP: product moduleRoot changed; inspect fixture root adaptation')
        text=text.replace(old,'filepath.Join(dir, "cmd", "objnode", "main.go")')
    dest=base/'fixture'/source.name
    if not dest.exists() or dest.read_text()!=text:
        dest.write_text(text)
(base/'evidence'/(sys.argv[1]+'.sources.sha256')).write_text(''.join(lines))
