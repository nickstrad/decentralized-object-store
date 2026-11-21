package saga

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type RecordKind uint8

const (
	RecBegin RecordKind = iota + 1
	RecIntent
	RecDone
	RecAbort
	RecCompIntent
	RecCompDone
	RecEnd
)

func (k RecordKind) String() string {
	switch k {
	case RecBegin:
		return "begin"
	case RecIntent:
		return "intent"
	case RecDone:
		return "done"
	case RecAbort:
		return "abort"
	case RecCompIntent:
		return "comp-intent"
	case RecCompDone:
		return "comp-done"
	case RecEnd:
		return "end"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

type Record struct {
	Kind    RecordKind
	Step    int
	Attempt int
	Time    env.Time
	Def     string
	Data    []byte
	State   State
}

const (
	MaxRecord  = 1 << 20
	frameMagic = 0xA5
	headerSize = 13
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func validRecord(r Record) error {
	switch {
	case r.Kind < RecBegin || r.Kind > RecEnd:
		return fmt.Errorf("record kind %d", r.Kind)
	case r.Step < -1 || r.Step > math.MaxInt32:
		return fmt.Errorf("record step %d", r.Step)
	case r.Attempt < 0 || r.Attempt > math.MaxInt32:
		return fmt.Errorf("record attempt %d", r.Attempt)
	case r.State > StateCompensated:
		return fmt.Errorf("record state %d", r.State)
	}
	return nil
}

func appendPayload(dst []byte, r Record) []byte {
	dst = append(dst, byte(r.Kind))
	dst = binary.AppendVarint(dst, int64(r.Step))
	dst = binary.AppendUvarint(dst, uint64(r.Attempt))
	dst = binary.AppendVarint(dst, int64(r.Time))
	dst = append(dst, byte(r.State))
	dst = binary.AppendUvarint(dst, uint64(len(r.Def)))
	dst = append(dst, r.Def...)
	dst = binary.AppendUvarint(dst, uint64(len(r.Data)))
	return append(dst, r.Data...)
}

func AppendRecord(dst []byte, r Record) ([]byte, error) {
	if err := validRecord(r); err != nil {
		return dst, fmt.Errorf("saga: encode %s: %w: %w", r.Kind, err, env.ErrInvalidArgument)
	}
	payload := appendPayload(nil, r)
	if len(payload) > MaxRecord {
		return dst, fmt.Errorf("saga: encode %s: %d bytes over %d: %w", r.Kind, len(payload), MaxRecord, env.ErrTooLarge)
	}
	var h [headerSize]byte
	h[0] = frameMagic
	binary.BigEndian.PutUint32(h[1:5], uint32(len(payload)))
	binary.BigEndian.PutUint32(h[5:9], crc32.Checksum(h[:5], castagnoli))
	binary.BigEndian.PutUint32(h[9:13], crc32.Checksum(payload, castagnoli))
	dst = append(dst, h[:]...)
	return append(dst, payload...), nil
}

type reader struct {
	b   []byte
	bad bool
}

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) varint() int64 {
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) byte() byte {
	if len(r.b) == 0 {
		r.bad = true
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) bytes() []byte {
	n := r.uvarint()
	if r.bad || n > uint64(len(r.b)) {
		r.bad = true
		return nil
	}
	out := bytes.Clone(r.b[:n])
	r.b = r.b[n:]
	return out
}

func decodePayload(p []byte) (Record, error) {
	rd := &reader{b: p}
	var rec Record
	rec.Kind = RecordKind(rd.byte())
	step := rd.varint()
	attempt := rd.uvarint()
	rec.Time = env.Time(rd.varint())
	rec.State = State(rd.byte())
	rec.Def = string(rd.bytes())
	rec.Data = rd.bytes()
	if rd.bad || len(rd.b) != 0 {
		return Record{}, fmt.Errorf("malformed payload")
	}
	if step < -1 || step > math.MaxInt32 || attempt > math.MaxInt32 {
		return Record{}, fmt.Errorf("step %d or attempt %d out of range", step, attempt)
	}
	rec.Step, rec.Attempt = int(step), int(attempt)
	if err := validRecord(rec); err != nil {
		return Record{}, err
	}
	if len(rec.Data) == 0 {
		rec.Data = nil
	}
	if !bytes.Equal(appendPayload(nil, rec), p) {
		return Record{}, fmt.Errorf("non-canonical payload")
	}
	return rec, nil
}

func corrupt(off int, why string) error {
	return fmt.Errorf("saga: record at offset %d: %s: %w", off, why, ErrLogCorrupt)
}

func DecodeLog(b []byte) ([]Record, int, error) {
	var recs []Record
	off := 0
	for off < len(b) {
		rest := b[off:]
		if rest[0] != frameMagic {
			return recs, off, corrupt(off, "bad magic")
		}
		if len(rest) < 9 {
			return recs, off, nil
		}
		if binary.BigEndian.Uint32(rest[5:9]) != crc32.Checksum(rest[:5], castagnoli) {
			return recs, off, corrupt(off, "header checksum")
		}
		n := binary.BigEndian.Uint32(rest[1:5])
		if n == 0 || n > MaxRecord {
			return recs, off, corrupt(off, "length "+strconv.FormatUint(uint64(n), 10))
		}
		if len(rest) < headerSize+int(n) {
			return recs, off, nil
		}
		payload := rest[headerSize : headerSize+int(n)]
		if binary.BigEndian.Uint32(rest[9:13]) != crc32.Checksum(payload, castagnoli) {
			return recs, off, corrupt(off, "payload checksum")
		}
		rec, err := decodePayload(payload)
		if err != nil {
			return recs, off, corrupt(off, err.Error())
		}
		recs = append(recs, rec)
		off += headerSize + int(n)
	}
	return recs, off, nil
}
