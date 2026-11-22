package gossip

func WithoutIndirectProbes(c Config) Config {
	c.noIndirect = true
	return c
}

func RelayCount(n *Node) int { return len(n.relays) }

func RelayBacklog(n *Node) int { return len(n.relayFIFO) }

func TombBacklog(n *Node) int { return len(n.tombFIFO) }

func SeedCount(n *Node) int { return len(n.seeds) }

func ViewBacklog(v *View) int { return len(v.ids) }
