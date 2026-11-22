package occ

func (n *Node) Merge(d *Doc) (bool, error) {
	if n.store == nil {
		return false, n.notReady("merge", "")
	}
	return n.store.Merge(d)
}
