package gossip

import (
	"fmt"
	"math/bits"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	DefaultProbeInterval  = time.Second
	DefaultProbeTimeout   = 400 * time.Millisecond
	DefaultIndirectProbes = 3
	DefaultSuspicionMult  = 4
	DefaultRetransmitMult = 4
	DefaultSyncInterval   = 15 * time.Second
	DefaultSyncTimeout    = 2 * time.Second
	DefaultDeadRetention  = 5 * time.Minute
	DefaultMaxPacket      = 1400

	MaxIndirectProbes = 64
	MaxMult           = 1000
	MaxPacketLimit    = 1 << 20
	MaxInterval       = time.Hour
)

type Config struct {
	Seeds          []env.NodeID
	ProbeInterval  time.Duration
	ProbeTimeout   time.Duration
	IndirectProbes int
	SuspicionMult  int
	RetransmitMult int
	SyncInterval   time.Duration
	SyncTimeout    time.Duration
	DeadRetention  time.Duration
	MaxPacket      int

	noIndirect bool
}

func (c Config) WithDefaults() Config {
	c.Seeds = slices.Clone(c.Seeds)
	if c.ProbeInterval == 0 {
		c.ProbeInterval = DefaultProbeInterval
	}
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = DefaultProbeTimeout
	}
	if c.IndirectProbes == 0 {
		c.IndirectProbes = DefaultIndirectProbes
	}
	if c.SuspicionMult == 0 {
		c.SuspicionMult = DefaultSuspicionMult
	}
	if c.RetransmitMult == 0 {
		c.RetransmitMult = DefaultRetransmitMult
	}
	if c.SyncInterval == 0 {
		c.SyncInterval = DefaultSyncInterval
	}
	if c.SyncTimeout == 0 {
		c.SyncTimeout = DefaultSyncTimeout
	}
	if c.DeadRetention == 0 {
		c.DeadRetention = DefaultDeadRetention
	}
	if c.MaxPacket == 0 {
		c.MaxPacket = DefaultMaxPacket
	}
	return c
}

func (c Config) Validate() error {
	bad := func(what string, v int64) error {
		return fmt.Errorf("gossip: config: %s %d out of range: %w", what, v, env.ErrInvalidArgument)
	}
	switch {
	case c.ProbeInterval <= 0 || c.ProbeInterval > MaxInterval:
		return bad("probe interval", int64(c.ProbeInterval))
	case c.ProbeTimeout <= 0 || c.ProbeTimeout >= c.ProbeInterval:
		return bad("probe timeout", int64(c.ProbeTimeout))
	case c.IndirectProbes < 1 || c.IndirectProbes > MaxIndirectProbes:
		return bad("indirect probes", int64(c.IndirectProbes))
	case c.SuspicionMult < 1 || c.SuspicionMult > MaxMult:
		return bad("suspicion multiplier", int64(c.SuspicionMult))
	case c.RetransmitMult < 1 || c.RetransmitMult > MaxMult:
		return bad("retransmit multiplier", int64(c.RetransmitMult))
	case c.SyncInterval <= 0 || c.SyncInterval > MaxInterval:
		return bad("sync interval", int64(c.SyncInterval))
	case c.SyncTimeout <= 0 || c.SyncTimeout > MaxInterval:
		return bad("sync timeout", int64(c.SyncTimeout))
	case c.DeadRetention <= 0 || c.DeadRetention > 1000*MaxInterval:
		return bad("dead retention", int64(c.DeadRetention))
	case c.MaxPacket < MinPacket || c.MaxPacket > MaxPacketLimit:
		return bad("max packet", int64(c.MaxPacket))
	}
	for i, s := range c.Seeds {
		if s == "" || len(s) > MaxIDLen {
			return bad("length of seed id at index", int64(i))
		}
	}
	return nil
}

func log2Ceil(n int) int {
	if n <= 2 {
		return 1
	}
	return bits.Len(uint(n - 1))
}

func (c Config) SuspicionTimeout(n int) time.Duration {
	return time.Duration(c.SuspicionMult*log2Ceil(n)) * c.ProbeInterval
}

func (c Config) RetransmitLimit(n int) int {
	return c.RetransmitMult * log2Ceil(n)
}
