package gossip_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/nickstrad/object-store/internal/gossip"
)

const fuzzMaxInput = 64 << 10

// FuzzGossipWire proves that Decode never panics, returns ErrMalformed or a
// canonical Message with Encode(Decode(b)) == b, and that feeding arbitrary
// bytes to OnDatagram and OnRequest preserves node invariants and packet bounds.
func FuzzGossipWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{gossip.Version})
	f.Add([]byte{gossip.Version, byte(gossip.KindPing), 0, 0, 0, 1, 0, 0})
	f.Add([]byte{gossip.Version, byte(gossip.KindSyncResp), 0, 0, 0, 0, 0xff, 0xff})
	for _, c := range wireCases() {
		b, err := c.msg.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	self, _ := gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{mem("self", 1<<32-1, gossip.StateDead)}}.Encode()
	f.Add(self)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInput {
			return
		}
		m, err := gossip.Decode(data)
		if err != nil {
			if !errors.Is(err, gossip.ErrMalformed) {
				t.Fatalf("Decode error %v is not ErrMalformed", err)
			}
		} else {
			again, err := m.Encode()
			if err != nil {
				t.Fatalf("decoded message does not re-encode: %v", err)
			}
			if !bytes.Equal(again, data) {
				t.Fatalf("decode then encode is not the identity:\n in  %x\n out %x", data, again)
			}
		}

		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01", "p02", "p03", "p04")
		h.node.Start()
		before := h.node.Self().Incarnation
		h.node.OnDatagram("p01", data)
		replied := 0
		h.node.OnRequest("p02", data, func(resp []byte, err error) {
			replied++
			if err == nil {
				if r, derr := gossip.Decode(resp); derr != nil || r.Kind != gossip.KindSyncResp {
					t.Fatalf("sync reply %x does not decode as a sync response: %v", resp, derr)
				}
			} else if !errors.Is(err, gossip.ErrMalformed) {
				t.Fatalf("request refused with %v", err)
			}
		})
		if replied != 1 {
			t.Fatalf("request answered %d times", replied)
		}
		h.clock.advance(3 * gossip.DefaultProbeInterval)
		self := h.node.Self()
		if self.State != gossip.StateAlive || self.Incarnation < before {
			t.Fatalf("invariant broken: self %+v, incarnation before %d", self, before)
		}
		all := h.node.All()
		if !slices.IsSortedFunc(all, func(a, b gossip.Member) int { return compareID(a.ID, b.ID) }) {
			t.Fatalf("view not sorted: %+v", all)
		}
		for i := 1; i < len(all); i++ {
			if all[i].ID == all[i-1].ID {
				t.Fatalf("duplicate member %s", all[i].ID)
			}
		}
		for _, s := range h.net.sent {
			if len(s.msg) > gossip.DefaultMaxPacket {
				t.Fatalf("sent %d bytes", len(s.msg))
			}
			if _, err := gossip.Decode(s.msg); err != nil {
				t.Fatalf("node sent an undecodable packet: %v", err)
			}
		}
	})
}
