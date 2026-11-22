package gossip

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Version is the wire protocol version byte carried at offset 0 of every packet.
const Version byte = 1

// Kind identifies the SWIM message type carried on a datagram or sync exchange.
type Kind uint8

// Wire message kinds. KindPing..KindGossip travel as datagrams; KindSyncReq
// and KindSyncResp travel only on the request/response path.
const (
	KindPing Kind = iota + 1
	KindAck
	KindPingReq
	KindGossip
	KindSyncReq
	KindSyncResp
)

// String returns the canonical hyphenated name of k, or "kind(N)" if unknown.
func (k Kind) String() string {
	switch k {
	case KindPing:
		return "ping"
	case KindAck:
		return "ack"
	case KindPingReq:
		return "ping-req"
	case KindGossip:
		return "gossip"
	case KindSyncReq:
		return "sync-req"
	case KindSyncResp:
		return "sync-resp"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

func (k Kind) valid() bool { return k >= KindPing && k <= KindSyncResp }

func (k Kind) datagram() bool { return k >= KindPing && k <= KindGossip }

// Wire framing limits:
//   - MaxIDLen: maximum byte length of a NodeID or Region (1-byte length prefix).
//   - MaxUpdates: maximum updates per message (uint16 count field, 65,535).
//   - MinPacket: minimum valid Config.MaxPacket (781 bytes), large enough for a
//     maximum-length ping-req header plus one maximum-length member update.
const (
	MaxIDLen      = 255
	MaxUpdates    = 1<<16 - 1
	headerSize    = 8
	minUpdateSize = 8
	maxUpdateSize = 7 + 2*MaxIDLen
	MinPacket     = headerSize + 1 + MaxIDLen + maxUpdateSize
)

// Message is the decoded form of a SWIM datagram or push-pull sync payload.
// Target is populated only when Kind is KindPingReq.
type Message struct {
	Kind    Kind
	Seq     uint32
	Target  env.NodeID
	Updates []Member
}

// UpdateSize returns the encoded wire length in bytes of a single Member update.
func UpdateSize(u Member) int { return 7 + len(u.ID) + len(u.Region) }

// Size returns the exact encoded wire length in bytes of m.
func (m Message) Size() int {
	n := headerSize
	if m.Kind == KindPingReq {
		n += 1 + len(m.Target)
	}
	for _, u := range m.Updates {
		n += UpdateSize(u)
	}
	return n
}

func invalid(what string, args ...any) error {
	return fmt.Errorf("gossip: encode: "+what+": %w", append(args, env.ErrInvalidArgument)...)
}

// Encode serializes m in canonical big-endian wire format:
// version(u8) | kind(u8) | seq(u32) | [target(len u8, bytes) if ping-req] |
// count(u16) | count*(state u8 | incarnation u32 | id | region).
// It returns env.ErrInvalidArgument if any field violates wire constraints.
func (m Message) Encode() ([]byte, error) {
	if !m.Kind.valid() {
		return nil, invalid("kind %d", uint8(m.Kind))
	}
	if m.Kind == KindPingReq {
		if m.Target == "" || len(m.Target) > MaxIDLen {
			return nil, invalid("ping-req target of %d bytes", len(m.Target))
		}
	} else if m.Target != "" {
		return nil, invalid("target on a %s message", m.Kind.String())
	}
	if len(m.Updates) > MaxUpdates {
		return nil, invalid("%d updates", len(m.Updates))
	}
	for i, u := range m.Updates {
		if u.ID == "" || len(u.ID) > MaxIDLen || len(u.Region) > MaxIDLen || !u.State.valid() {
			return nil, invalid("update %d", i)
		}
	}
	b := make([]byte, 0, m.Size())
	b = append(b, Version, byte(m.Kind))
	b = binary.BigEndian.AppendUint32(b, m.Seq)
	if m.Kind == KindPingReq {
		b = appendStr(b, string(m.Target))
	}
	b = binary.BigEndian.AppendUint16(b, uint16(len(m.Updates)))
	for _, u := range m.Updates {
		b = append(b, byte(u.State))
		b = binary.BigEndian.AppendUint32(b, u.Incarnation)
		b = appendStr(b, string(u.ID))
		b = appendStr(b, string(u.Region))
	}
	return b, nil
}

func appendStr(b []byte, s string) []byte {
	b = append(b, byte(len(s)))
	return append(b, s...)
}

type decoder struct {
	b     []byte
	off   int
	err   error
	known func(id []byte) (Member, bool)
}

func (d *decoder) fail(what string) {
	if d.err == nil {
		d.err = fmt.Errorf("gossip: decode: %s at offset %d of %d: %w", what, d.off, len(d.b), ErrMalformed)
	}
}

func (d *decoder) take(n int, what string) []byte {
	if d.err != nil {
		return nil
	}
	if n > len(d.b)-d.off {
		d.fail("truncated " + what)
		return nil
	}
	out := d.b[d.off : d.off+n]
	d.off += n
	return out
}

func (d *decoder) u8(what string) byte {
	if b := d.take(1, what); b != nil {
		return b[0]
	}
	return 0
}

func (d *decoder) u16(what string) uint16 {
	if b := d.take(2, what); b != nil {
		return binary.BigEndian.Uint16(b)
	}
	return 0
}

func (d *decoder) u32(what string) uint32 {
	if b := d.take(4, what); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

func (d *decoder) str(lenWhat, what string) string {
	n := int(d.u8(lenWhat))
	return string(d.take(n, what))
}

// Decode parses a canonical wire-encoded Message from b. It rejects unknown
// versions, kinds, or states, empty IDs or targets, truncated payloads, counts
// exceeding the remaining bytes, and trailing bytes with ErrMalformed.
// Returned strings are copied out of b so the caller may reuse b safely.
func Decode(b []byte) (Message, error) {
	return decodeInto(b, nil, nil)
}

func (d *decoder) member() Member {
	var u Member
	u.State = State(d.u8("state"))
	if d.err == nil && !u.State.valid() {
		d.fail("unknown state " + strconv.Itoa(int(u.State)))
	}
	u.Incarnation = d.u32("incarnation")
	id := d.take(int(d.u8("member id length")), "member id")
	if d.err == nil && len(id) == 0 {
		d.fail("empty member id")
	}
	region := d.take(int(d.u8("region length")), "region")
	if d.err != nil {
		return u
	}
	// Reuse existing immutable strings for known members to avoid per-datagram
	// string allocations without aliasing the inbound byte buffer.
	if d.known != nil {
		if k, ok := d.known(id); ok {
			u.ID = k.ID
			if string(region) == string(k.Region) {
				u.Region = k.Region
				return u
			}
			u.Region = env.Region(region)
			return u
		}
	}
	u.ID = env.NodeID(id)
	u.Region = env.Region(region)
	return u
}

// decodeInto parses b into an optional reusable update slice dst, reusing known
// member strings through known when provided.
func decodeInto(b []byte, dst []Member, known func(id []byte) (Member, bool)) (Message, error) {
	d := &decoder{b: b, known: known}
	var m Message
	if v := d.u8("version"); d.err == nil && v != Version {
		d.fail("unknown version " + strconv.Itoa(int(v)))
	}
	m.Kind = Kind(d.u8("kind"))
	if d.err == nil && !m.Kind.valid() {
		d.fail("unknown kind " + strconv.Itoa(int(m.Kind)))
	}
	m.Seq = d.u32("sequence number")
	if m.Kind == KindPingReq {
		m.Target = env.NodeID(d.str("target length", "target"))
		if d.err == nil && m.Target == "" {
			d.fail("empty target")
		}
	}
	count := int(d.u16("update count"))
	// Check the minimum byte requirement before allocating m.Updates so a
	// short packet claiming MaxUpdates cannot force a large allocation.
	if d.err == nil && count*minUpdateSize > len(b)-d.off {
		d.fail("update count " + strconv.Itoa(count) + " exceeds the packet")
	}
	if d.err == nil && count > 0 {
		if cap(dst) >= count {
			m.Updates = dst[:0]
		} else {
			m.Updates = make([]Member, 0, count)
		}
	}
	for i := 0; i < count && d.err == nil; i++ {
		u := d.member()
		if d.err == nil {
			m.Updates = append(m.Updates, u)
		}
	}
	// Reject trailing bytes so Encode(Decode(b)) == b holds for every accepted input.
	if d.err == nil && d.off != len(b) {
		d.fail(strconv.Itoa(len(b)-d.off) + " trailing bytes")
	}
	if d.err != nil {
		return Message{}, d.err
	}
	return m, nil
}
