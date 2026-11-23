package storage

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"

	"github.com/nickstrad/object-store/internal/env"
)

// Member pairs a node's identifier with its failure-domain region.
type Member struct {
	ID     env.NodeID
	Region env.Region
}

// Membership supplies the current set of live cluster members for shard
// placement. Implementations may return members in any order.
type Membership interface {
	Members() []Member
}

// StaticMembership is a fixed Membership slice used when membership does not
// come from a live gossip view.
type StaticMembership []Member

// Members returns a defensive copy of s.
func (s StaticMembership) Members() []Member { return slices.Clone(s) }

// StripeKey returns the rendezvous hashing key "<bucket>/<version-hex16>/<stripe>"
// for one stripe of an object version.
func StripeKey(bucket string, v VersionID, stripe uint32) string {
	return bucket + "/" + v.String() + "/" + strconv.FormatUint(uint64(stripe), 10)
}

type scored struct {
	Member
	score uint64
}

// score computes the rendezvous weight as the big-endian first 8 bytes of
// SHA-256(key || 0x00 || id).
func score(key string, id env.NodeID) uint64 {
	h := sha256.New()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	var sum [32]byte
	h.Sum(sum[:0])
	return binary.BigEndian.Uint64(sum[:8])
}

func sortedMembers(ms Membership) ([]Member, error) {
	if ms == nil {
		return nil, fmt.Errorf("storage: placement: no membership: %w", ErrInvalid)
	}
	// Clone before sorting so a Membership returning a cached slice is never mutated.
	members := slices.Clone(ms.Members())
	slices.SortFunc(members, func(a, b Member) int { return cmp.Compare(a.ID, b.ID) })
	for i, m := range members {
		if m.ID == "" {
			return nil, fmt.Errorf("storage: placement: empty member id: %w", ErrInvalid)
		}
		if i > 0 && members[i-1].ID == m.ID {
			return nil, fmt.Errorf("storage: placement: member %s listed twice: %w", m.ID, ErrInvalid)
		}
	}
	return members, nil
}

func regionSizes(members []Member) []int {
	regions := make([]env.Region, len(members))
	for i, m := range members {
		regions[i] = m.Region
	}
	slices.Sort(regions)
	var sizes []int
	for i := range regions {
		if i == 0 || regions[i] != regions[i-1] {
			sizes = append(sizes, 0)
		}
		sizes[len(sizes)-1]++
	}
	return sizes
}

// RegionCap returns the maximum number of shards any single region may hold
// when placing n shards with parity count m across ms, or 0 if n < 1 or ms has
// fewer than n members.
func RegionCap(ms StaticMembership, n, m int) int {
	return regionCap(regionSizes(ms), n, m)
}

// regionCap returns max(m, c*), where c* is the smallest per-region cap whose
// regions can hold n shards. Using max(c*, m) keeps at most m shards per region
// when regions allow, spreads evenly otherwise, and avoids cap churn on small
// membership changes.
func regionCap(sizes []int, n, m int) int {
	total := 0
	for _, s := range sizes {
		total += s
	}
	if n < 1 || total < n {
		return 0
	}
	for c := 1; ; c++ {
		sum := 0
		for _, s := range sizes {
			sum += min(s, c)
		}
		if sum >= n {
			return max(c, m)
		}
	}
}

func rank(members []Member, key string) []scored {
	out := make([]scored, len(members))
	for i, m := range members {
		out[i] = scored{Member: m, score: score(key, m.ID)}
	}
	slices.SortFunc(out, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

func prepare(ms Membership, n, m int) ([]Member, int, error) {
	if n < 1 || m < 0 {
		return nil, 0, fmt.Errorf("storage: placement: n=%d m=%d: %w", n, m, ErrInvalid)
	}
	members, err := sortedMembers(ms)
	if err != nil {
		return nil, 0, err
	}
	if len(members) < n {
		return nil, 0, fmt.Errorf("storage: placement: %d members for %d shards: %w", len(members), n, ErrUnavailable)
	}
	return members, regionCap(regionSizes(members), n, m), nil
}

// regionCount tracks per-region shard counts in parallel slices rather than a
// map to preserve deterministic iteration and avoid map overhead.
type regionCount struct {
	regions []env.Region
	counts  []int
}

func (r *regionCount) find(reg env.Region) int {
	for i, x := range r.regions {
		if x == reg {
			return i
		}
	}
	r.regions = append(r.regions, reg)
	r.counts = append(r.counts, 0)
	return len(r.regions) - 1
}

// Place chooses n distinct members from ms for key by rendezvous hashing,
// capping shards per region at RegionCap(..., n, m) so no single region holds
// more than m shards when the cluster's regions allow. Result order is
// deterministic and independent of ms's slice order. It returns ErrInvalid for
// nil ms, bad n/m, or empty/duplicate member IDs, and ErrUnavailable if fewer
// than n members exist.
func Place(ms Membership, key string, n, m int) ([]env.NodeID, error) {
	members, capacity, err := prepare(ms, n, m)
	if err != nil {
		return nil, err
	}
	out := make([]env.NodeID, 0, n)
	var rc regionCount
	for _, c := range rank(members, key) {
		if len(out) == n {
			break
		}
		i := rc.find(c.Region)
		if rc.counts[i] >= capacity {
			continue
		}
		rc.counts[i]++
		out = append(out, c.ID)
	}
	return out, nil
}

// Replace updates an existing shard placement current for repair against ms.
// Every shard whose holder is still a member (and within the region cap and
// not duplicated) stays at its existing index; only vacated slots are filled
// from the rendezvous ranking for key. Error semantics match Place.
func Replace(ms Membership, key string, current []env.NodeID, m int) ([]env.NodeID, error) {
	n := len(current)
	members, capacity, err := prepare(ms, n, m)
	if err != nil {
		return nil, err
	}
	regionOf := func(id env.NodeID) (env.Region, bool) {
		i, ok := slices.BinarySearchFunc(members, id, func(a Member, t env.NodeID) int { return cmp.Compare(a.ID, t) })
		if !ok {
			return "", false
		}
		return members[i].Region, true
	}
	out := make([]env.NodeID, n)
	var rc regionCount
	for i, id := range current {
		reg, ok := regionOf(id)
		if !ok || slices.Contains(out[:i], id) {
			continue
		}
		j := rc.find(reg)
		if rc.counts[j] >= capacity {
			continue
		}
		rc.counts[j]++
		out[i] = id
	}
	ranked := rank(members, key)
	next := 0
	for i := range out {
		if out[i] != "" {
			continue
		}
		for ; next < len(ranked); next++ {
			c := ranked[next]
			j := rc.find(c.Region)
			if rc.counts[j] >= capacity || slices.Contains(out, c.ID) {
				continue
			}
			rc.counts[j]++
			out[i] = c.ID
			break
		}
		if out[i] == "" {
			return nil, fmt.Errorf("storage: replace: no member left for shard %d: %w", i, ErrUnavailable)
		}
	}
	return out, nil
}
