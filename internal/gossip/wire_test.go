package gossip_test

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
)

const goldenWire = "wire-golden.txt"

func wireCases() []struct {
	name string
	msg  gossip.Message
} {
	long := gossip.Member{ID: env.NodeID(strings.Repeat("i", gossip.MaxIDLen)), Region: env.Region(strings.Repeat("r", gossip.MaxIDLen)), Incarnation: 1<<32 - 1, State: gossip.StateLeft}
	return []struct {
		name string
		msg  gossip.Message
	}{
		{"ping-empty", gossip.Message{Kind: gossip.KindPing, Seq: 1}},
		{"ping-piggyback", gossip.Message{Kind: gossip.KindPing, Seq: 0xdeadbeef, Updates: []gossip.Member{
			{ID: "n001", Region: "r1", Incarnation: 3, State: gossip.StateAlive},
			{ID: "n002", Region: "r2", Incarnation: 0, State: gossip.StateSuspect},
		}}},
		{"ack", gossip.Message{Kind: gossip.KindAck, Seq: 7, Updates: []gossip.Member{{ID: "x", Incarnation: 9, State: gossip.StateDead}}}},
		{"ping-req", gossip.Message{Kind: gossip.KindPingReq, Seq: 42, Target: "n009"}},
		{"gossip-left", gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{{ID: "n003", Region: "r0", Incarnation: 2, State: gossip.StateLeft}}}},
		{"sync-req", gossip.Message{Kind: gossip.KindSyncReq, Updates: []gossip.Member{{ID: "a", Region: "r0", State: gossip.StateAlive}}}},
		{"sync-resp-empty", gossip.Message{Kind: gossip.KindSyncResp}},
		{"max-lengths", gossip.Message{Kind: gossip.KindPingReq, Seq: 1<<32 - 1, Target: long.ID, Updates: []gossip.Member{long}}},
	}
}

// TestWireRoundTrip proves exact Size accounting, lossless encode/decode round
// trips, canonical re-encoding, and golden wire bytes in testdata/wire-golden.txt.
func TestWireRoundTrip(t *testing.T) {
	golden := map[string]string{}
	path := filepath.Join("testdata", goldenWire)
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			name, hx, ok := strings.Cut(sc.Text(), " ")
			if ok {
				golden[name] = hx
			}
		}
		if err := errors.Join(sc.Err(), f.Close()); err != nil {
			t.Fatal(err)
		}
	}
	update := os.Getenv("GOSSIP_UPDATE_GOLDEN") == "1"
	var out strings.Builder
	for _, c := range wireCases() {
		t.Run(c.name+" survives encode then decode unchanged", func(t *testing.T) {
			b, err := c.msg.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if len(b) != c.msg.Size() {
				t.Fatalf("Size() = %d, encoded %d bytes", c.msg.Size(), len(b))
			}
			got, err := gossip.Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normalize(got), normalize(c.msg)) {
				t.Fatalf("round trip: got %+v, want %+v", got, c.msg)
			}
			again, err := got.Encode()
			if err != nil || hex.EncodeToString(again) != hex.EncodeToString(b) {
				t.Fatalf("re-encode differs: %x vs %x (%v)", again, b, err)
			}
			fmt.Fprintf(&out, "%s %s\n", c.name, hex.EncodeToString(b))
			if !update && golden[c.name] != hex.EncodeToString(b) {
				t.Fatalf("wire format changed for %s:\n got  %x\n want %s\n(set GOSSIP_UPDATE_GOLDEN=1 to rewrite testdata/%s on purpose)", c.name, b, golden[c.name], goldenWire)
			}
		})
	}
	if update {
		if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func normalize(m gossip.Message) gossip.Message {
	if len(m.Updates) == 0 {
		m.Updates = nil
	}
	return m
}

// TestDecodeMalformed proves that truncated, unknown-field, over-counted,
// empty-ID/target, and trailing-byte packets are rejected with ErrMalformed.
func TestDecodeMalformed(t *testing.T) {
	valid := func(m gossip.Message) []byte {
		b, err := m.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ping := valid(gossip.Message{Kind: gossip.KindPing, Seq: 1, Updates: []gossip.Member{mem("ab", 1, gossip.StateAlive)}})
	pingReq := valid(gossip.Message{Kind: gossip.KindPingReq, Seq: 1, Target: "t"})
	mut := func(b []byte, i int, v byte) []byte {
		c := append([]byte(nil), b...)
		c[i] = v
		return c
	}
	updOff := 8
	rows := []struct {
		name string
		in   []byte
	}{
		{"an empty packet", nil},
		{"a lone version byte", []byte{gossip.Version}},
		{"an unknown version", mut(ping, 0, gossip.Version+1)},
		{"kind zero", mut(ping, 1, 0)},
		{"an unknown kind", mut(ping, 1, 7)},
		{"a truncated sequence number", ping[:4]},
		{"a missing update count", ping[:6]},
		{"a half update count", ping[:7]},
		{"a count larger than the updates present", ping[:len(ping)-1]},
		{"trailing bytes after the last update", append(append([]byte(nil), ping...), 0)},
		{"state zero", mut(ping, updOff, 0)},
		{"an unknown state", mut(ping, updOff, 5)},
		{"a truncated incarnation", ping[:updOff+3]},
		{"an empty member id", valid2(t)},
		{"an id length past the end", mut(ping, updOff+5, 200)},
		{"a region length past the end", mut(ping, updOff+8, 200)},
		{"a ping-req with an empty target", mut(pingReq, 6, 0)},
		{"a ping-req with a truncated target", pingReq[:7]},
		{"a count claiming 65535 updates in a short packet", []byte{gossip.Version, byte(gossip.KindSyncResp), 0, 0, 0, 0, 0xff, 0xff, 1}},
	}
	for _, r := range rows {
		t.Run(r.name+" is rejected as malformed", func(t *testing.T) {
			_, err := gossip.Decode(r.in)
			if !errors.Is(err, gossip.ErrMalformed) {
				t.Fatalf("Decode(%x) error = %v, want ErrMalformed", r.in, err)
			}
		})
	}
}

// valid2 crafts a wire ping whose single update has an empty member ID.
func valid2(t *testing.T) []byte {
	t.Helper()
	b, err := gossip.Message{Kind: gossip.KindPing, Seq: 1, Updates: []gossip.Member{mem("a", 1, gossip.StateAlive)}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	c := append([]byte(nil), b[:13]...)
	c = append(c, 0)
	return append(c, b[15:]...)
}

// TestEncodeRejectsInvalidMessages proves that Encode refuses invalid kinds,
// misplaced or over-long targets, bad updates, and counts above MaxUpdates with
// env.ErrInvalidArgument.
func TestEncodeRejectsInvalidMessages(t *testing.T) {
	long := env.NodeID(strings.Repeat("x", gossip.MaxIDLen+1))
	many := make([]gossip.Member, gossip.MaxUpdates+1)
	for i := range many {
		many[i] = mem(fmt.Sprintf("m%d", i), 0, gossip.StateAlive)
	}
	rows := []struct {
		name string
		msg  gossip.Message
	}{
		{"an unknown kind", gossip.Message{Kind: 0}},
		{"a ping with a target", gossip.Message{Kind: gossip.KindPing, Target: "t"}},
		{"a ping-req without a target", gossip.Message{Kind: gossip.KindPingReq}},
		{"a ping-req with an over-long target", gossip.Message{Kind: gossip.KindPingReq, Target: long}},
		{"an update with an empty id", gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{{State: gossip.StateAlive}}}},
		{"an update with an over-long id", gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{{ID: long, State: gossip.StateAlive}}}},
		{"an update with an over-long region", gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{{ID: "a", Region: env.Region(long), State: gossip.StateAlive}}}},
		{"an update with state zero", gossip.Message{Kind: gossip.KindGossip, Updates: []gossip.Member{{ID: "a"}}}},
		{"more updates than the count field holds", gossip.Message{Kind: gossip.KindSyncResp, Updates: many}},
	}
	for _, r := range rows {
		t.Run(r.name+" is refused", func(t *testing.T) {
			if _, err := r.msg.Encode(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("Encode error = %v, want env.ErrInvalidArgument", err)
			}
		})
	}
}
