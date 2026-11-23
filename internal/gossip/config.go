package gossip

import (
	"fmt"
	"math/bits"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// Protocol defaults and validation bounds:
//   - DefaultProbeInterval: protocol period; one peer is probed each period.
//   - DefaultProbeTimeout: direct-ack wait before indirect ping-req; must be below ProbeInterval.
//   - DefaultIndirectProbes: number of peers asked to relay a timed-out probe (k).
//   - DefaultSuspicionMult: suspicion timeout multiplier on ceil(log2 N) * ProbeInterval.
//   - DefaultRetransmitMult: piggyback transmission multiplier on ceil(log2 N) (lambda).
//   - DefaultSyncInterval: period for push-pull full-state sync and partition-heal sync.
//   - DefaultSyncTimeout: request timeout for sync and join exchanges.
//   - DefaultDeadRetention: duration dead and left entries stay in the view before burial.
//   - DefaultMaxPacket: datagram byte budget for outbound and inbound packets.
//   - MaxIndirectProbes, MaxMult, MaxPacketLimit, MaxInterval: upper bounds enforced by Validate.
//   - MaxTombstones: maximum buried verdicts kept per node before evicting the oldest.
//   - MaxRelays: maximum concurrent in-flight ping-req relays kept per node.
//   - MaxSeeds: maximum distinct non-self seed addresses accepted or remembered per node.
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
	MaxTombstones     = 4096
	MaxRelays         = 1024
	MaxSeeds          = 256
)

// Config configures a Node. Zero numeric or duration fields select the
// corresponding Default* constant (MaxMembers defaults to MaxUpdates).
// MaxPacket is assumed uniform across the cluster: inbound datagrams larger
// than MaxPacket are dropped as malformed. MaxMembers bounds the view entry
// count (including self) so sync messages fit the wire count field; deployments
// with long IDs or regions should set it lower to stay within transport limits.
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
	MaxMembers     int

	noIndirect bool
}

// WithDefaults returns a copy of c with a cloned Seeds slice and zero fields
// replaced by their defaults.
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
	if c.MaxMembers == 0 {
		c.MaxMembers = MaxUpdates
	}
	return c
}

// Validate checks that every field of c (typically after WithDefaults) lies
// within its protocol bounds. It returns an error wrapping env.ErrInvalidArgument
// if any field or seed ID is invalid.
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
	case c.MaxMembers < 1 || c.MaxMembers > MaxUpdates:
		return bad("max members", int64(c.MaxMembers))
	case len(c.Seeds) > MaxSeeds:
		return bad("seed count", int64(len(c.Seeds)))
	}
	for i, s := range c.Seeds {
		if s == "" || len(s) > MaxIDLen {
			return bad("length of seed id at index", int64(i))
		}
	}
	return nil
}

// log2Ceil computes max(1, ceil(log2(n))) in integer arithmetic so scaling
// decisions never depend on floating point.
func log2Ceil(n int) int {
	if n <= 2 {
		return 1
	}
	return bits.Len(uint(n - 1))
}

// SuspicionTimeout returns how long a suspected member stays suspect before
// being declared dead in a cluster of n live members: SuspicionMult * max(1, ceil(log2(n))) * ProbeInterval.
func (c Config) SuspicionTimeout(n int) time.Duration {
	return time.Duration(c.SuspicionMult*log2Ceil(n)) * c.ProbeInterval
}

// RetransmitLimit returns how many times a queued update is piggybacked before
// leaving the dissemination queue in a cluster of n live members: RetransmitMult * max(1, ceil(log2(n))).
func (c Config) RetransmitLimit(n int) int {
	return c.RetransmitMult * log2Ceil(n)
}
