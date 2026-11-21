package gossip

func WithoutIndirectProbes(c Config) Config {
	c.noIndirect = true
	return c
}

func RelayCount(n *Node) int { return len(n.relays) }
