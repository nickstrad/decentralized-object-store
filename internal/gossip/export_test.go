package gossip

func WithoutIndirectProbes(c Config) Config {
	c.noIndirect = true
	return c
}
