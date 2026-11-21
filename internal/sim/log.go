package sim

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const recordSize = 38

type record struct {
	at   env.Time
	seq  uint64
	kind evKind
	disp disposition
	node int
	peer int
	hash uint64
	aux  uint64
}

func (r record) bytes() [recordSize]byte {
	var b [recordSize]byte
	binary.BigEndian.PutUint64(b[0:], uint64(r.at))
	binary.BigEndian.PutUint64(b[8:], r.seq)
	b[16] = byte(r.kind)
	b[17] = byte(r.disp)
	binary.BigEndian.PutUint16(b[18:], uint16(r.node))
	binary.BigEndian.PutUint16(b[20:], uint16(r.peer))
	binary.BigEndian.PutUint64(b[22:], r.hash)
	binary.BigEndian.PutUint64(b[30:], r.aux)
	return b
}

func (s *Sim) record(e *event, disp disposition) {
	r := record{at: s.now, seq: e.seq, kind: e.kind, disp: disp, node: e.node, peer: e.peer}
	switch e.kind {
	case kindDatagram, kindRequest, kindResponse, kindRefused:
		r.hash = fnv64a(e.payload) ^ uint64(e.code)<<56
		r.aux = e.call
	case kindTimeout:
		r.aux = e.call
	case kindStart:
		r.aux = uint64(e.inc)
	case kindFault:
		r.hash = fnv64a(e.fault.Encode())
	case kindAction:
		r.hash = fnv64a([]byte(e.label))
	}
	b := r.bytes()
	_, _ = s.logHash.Write(b[:])
	s.logTotal++
	if len(s.logBuf) < s.cfg.LogCap {
		s.logBuf = append(s.logBuf, r)
		return
	}
	s.logBuf[s.logStart] = r
	s.logStart = (s.logStart + 1) % len(s.logBuf)
}

func (s *Sim) nodeName(i int) string {
	if i < 0 || i >= len(s.nodes) {
		return "-"
	}
	return string(s.nodes[i].id)
}

func (s *Sim) format(r record) string {
	return fmt.Sprintf("t=%d seq=%d %s/%s node=%s peer=%s hash=%016x aux=%d",
		r.at, r.seq, r.kind, r.disp, s.nodeName(r.node), s.nodeName(r.peer), r.hash, r.aux)
}

func (s *Sim) kept(i int) record {
	return s.logBuf[(s.logStart+i)%len(s.logBuf)]
}

func (s *Sim) LogTail(n int) []string {
	if n > len(s.logBuf) {
		n = len(s.logBuf)
	}
	out := make([]string, 0, n)
	for i := len(s.logBuf) - n; i < len(s.logBuf); i++ {
		out = append(out, s.format(s.kept(i)))
	}
	return out
}

func (s *Sim) LogLen() uint64 { return s.logTotal }

func (s *Sim) LogTruncated() bool { return s.logTotal > uint64(len(s.logBuf)) }

func (s *Sim) WriteLog(w io.Writer) error {
	bw := bufio.NewWriter(w)
	if s.LogTruncated() {
		if _, err := fmt.Fprintf(bw, "# log truncated: first %d of %d records dropped\n", s.logTotal-uint64(len(s.logBuf)), s.logTotal); err != nil {
			return fmt.Errorf("sim: write log: %w", err)
		}
	}
	for i := 0; i < len(s.logBuf); i++ {
		if _, err := fmt.Fprintln(bw, s.format(s.kept(i))); err != nil {
			return fmt.Errorf("sim: write log: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("sim: write log: %w", err)
	}
	return nil
}

type Digests struct {
	Events [32]byte
	State  [32]byte
}

func (d Digests) String() string {
	return fmt.Sprintf("events=%x state=%x", d.Events, d.State)
}

func (s *Sim) Digests() Digests {
	var d Digests
	copy(d.Events[:], s.logHash.Sum(nil))
	h := sha256.New()
	var buf []byte
	for _, n := range s.nodes {
		buf = buf[:0]
		buf = appendBytes(buf, []byte(n.id))
		buf = appendBytes(buf, []byte(n.region))
		flags := byte(0)
		if n.up {
			flags |= 1
		}
		if n.paused {
			flags |= 2
		}
		if n.started {
			flags |= 4
		}
		buf = append(buf, flags)
		buf = binary.AppendUvarint(buf, uint64(n.inc))
		var state []byte
		if n.up && n.started && n.proc != nil {
			s.guard(n.idx, "encode state", func() { state = n.proc.EncodeState() })
		}
		buf = appendBytes(buf, state)
		buf = n.disk.encode(buf)
		_, _ = h.Write(buf)
	}
	copy(d.State[:], h.Sum(nil))
	return d
}
