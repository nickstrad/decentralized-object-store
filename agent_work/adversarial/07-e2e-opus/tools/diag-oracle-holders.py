"""Diagnostic (07-e2e-opus): make the loss oracle print the holders of an unknown version.
Usage: python3 -I diag-oracle-holders.py <scratch-tree>"""
import sys
p=sys.argv[1]+'/test/e2e/simoracle_test.go'
s=open(p).read()
old='''	if man == nil {
		o.fail(fmt.Errorf("%s declared %s of %s/%s lost, a version no node ever committed", id, parts[2], parts[0], parts[1]))
		return
	}'''
new='''	if man == nil {
		for _, x := range o.c.ids {
			nd := o.c.anyNode(x)
			if nd == nil {
				continue
			}
			rd, err := nd.OCC().Reader()
			if err != nil {
				continue
			}
			mk := occ.ManifestKey(parts[0], parts[1])
			if v2, ok := rd.Record(mk).Version(); ok {
				if m2 := manifestOf(mk, v2); m2 != nil && m2.Version.String() == strings.TrimPrefix(parts[2], "~lost/") {
					man = m2
					break
				}
			}
		}
		if man != nil {
			for s := range man.Stripes {
				var hs []string
				for i, ref := range man.Stripes[s].Shards {
					d := o.c.s.Disk(ref.Node)
					st := "nodisk"
					if d != nil {
						if b, ok := d.Peek(man.ShardID(s, i).BlobName()); !ok {
							st = "absent"
						} else if storage.BlobChecksum(b) != ref.Checksum {
							st = "badsum"
						} else {
							st = "ok"
						}
					}
					hs = append(hs, fmt.Sprintf("%s(%s,up=%v)=%s", ref.Node, o.c.s.Region(ref.Node), o.c.node(ref.Node) != nil, st))
				}
				fmt.Printf("OPUSDIAG t=%v lost %s/%s v=%s stripe %d verifiable=%d K=%d holders %v\\n", o.c.s.Now(), parts[0], parts[1], man.Version, s, o.verifiable(man, s), man.Layout.K, hs)
			}
		}
		for _, x := range o.c.ids {
			if nd := o.c.anyNode(x); nd != nil {
				for _, sid := range nd.Engine().IDs() {
					st, err := nd.Engine().Status(sid)
					fmt.Printf("OPUSDIAG saga on %s: %s %+v %v\n", x, sid, st, err)
				}
			}
		}
		o.fail(fmt.Errorf("%s declared %s of %s/%s lost, a version no node ever committed", id, parts[2], parts[0], parts[1]))
		return
	}'''
assert old in s
open(p,'w').write(s.replace(old,new))
