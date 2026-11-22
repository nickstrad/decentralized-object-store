package gossip

// WithoutIndirectProbes returns a copy of c with indirect ping-req probing
// disabled so canary tests can verify that asymmetric-link detection fails
// without indirect probes.
func WithoutIndirectProbes(c Config) Config {
	c.noIndirect = true
	return c
}

// RelayCount returns the number of active pending ping-req relays held by n.
func RelayCount(n *Node) int { return len(n.relays) }

// RelayBacklog returns the length of n's relay expiry FIFO, including stale
// entries awaiting compaction.
func RelayBacklog(n *Node) int { return len(n.relayFIFO) }

// TombBacklog returns the length of n's tombstone expiry FIFO, including stale
// entries awaiting compaction.
func TombBacklog(n *Node) int { return len(n.tombFIFO) }

// SeedCount returns the number of remembered seed addresses held by n.
func SeedCount(n *Node) int { return len(n.seeds) }

// ViewBacklog returns the length of v's internal ID slice, including stale
// entries awaiting lazy compaction.
func ViewBacklog(v *View) int { return len(v.ids) }
