package occ

// Merge folds d into the node's metadata store using last-writer-wins
// reconciliation, recording any discards and persisting the result. It returns
// true if local state changed, or ErrNotReady before Start has loaded the
// store.
func (n *Node) Merge(d *Doc) (bool, error) {
	if n.store == nil {
		return false, n.notReady("merge", "")
	}
	return n.store.Merge(d)
}
