package node

import (
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// Leading channel byte prepended to every multiplexed message on env.Transport.
const (
	chGossip  byte = 1
	chStorage byte = 2
	chOCC     byte = 3
	chMeta    byte = 4
	chNode    byte = 5
)

// chanNet prefixes outbound datagrams and requests with one channel byte so all
// subsystems share one env.Transport.
type chanNet struct {
	net env.Transport
	ch  byte
}

func withChannel(ch byte, msg []byte) []byte {
	out := make([]byte, 1+len(msg))
	out[0] = ch
	copy(out[1:], msg)
	return out
}

func (c chanNet) Send(to env.NodeID, msg []byte) error {
	return c.net.Send(to, withChannel(c.ch, msg))
}

func (c chanNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	c.net.Request(to, withChannel(c.ch, req), timeout, cb)
}

// gossipView adapts gossip.Node's live membership to storage.Membership,
// merkle.Membership, and occ.Membership.
type gossipView struct {
	g    *gossip.Node
	self env.NodeID
}

func (v *gossipView) Members() []storage.Member {
	ms := v.g.Members()
	out := make([]storage.Member, 0, len(ms))
	for _, m := range ms {
		out = append(out, storage.Member{ID: m.ID, Region: m.Region})
	}
	return out
}

func (v *gossipView) Peers() []env.NodeID {
	ms := v.g.Members()
	out := make([]env.NodeID, 0, len(ms))
	for _, m := range ms {
		if m.ID != v.self {
			out = append(out, m.ID)
		}
	}
	return out
}

func (v *gossipView) InRegion(r env.Region) []env.NodeID {
	var out []env.NodeID
	for _, m := range v.g.Members() {
		if m.Region == r {
			out = append(out, m.ID)
		}
	}
	return out
}

// regionView filters gossipView.Members to one region for region-routed buckets.
type regionView struct {
	v      *gossipView
	region env.Region
}

func (r regionView) Members() []storage.Member {
	var out []storage.Member
	for _, m := range r.v.Members() {
		if m.Region == r.region {
			out = append(out, m)
		}
	}
	return out
}

// MaxFrame is the maximum TCP wire frame size in bytes (16 MiB), shared with
// the real runtime and checked against shard request sizes during Config.Validate.
const MaxFrame = 16 << 20
