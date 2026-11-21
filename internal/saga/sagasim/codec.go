package sagasim

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	MaxObjects = 4096
	MaxKey     = 128
	MaxValue   = 512
	MaxName    = 1024
	MaxRegs    = 64
	MaxBuckets = 64
)

func tooLarge(what string, n, max int) error {
	return fmt.Errorf("sagasim: %s of %d, at most %d: %w", what, n, max, env.ErrTooLarge)
}

func checkKV(kv KV) error {
	if len(kv.Key) > MaxKey {
		return tooLarge("object key", len(kv.Key), MaxKey)
	}
	if len(kv.Val) > MaxValue {
		return tooLarge("object value", len(kv.Val), MaxValue)
	}
	return nil
}

func checkName(what, s string) error {
	if len(s) > MaxName {
		return tooLarge(what, len(s), MaxName)
	}
	return nil
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("sagasim: %s: %w", what, ErrMalformed)
	}
}

func (r *reader) count(what string, max int) int {
	if r.err != nil {
		return 0
	}
	n, k := binary.Uvarint(r.b)
	if k <= 0 || n > uint64(max) {
		r.fail(what)
		return 0
	}
	r.b = r.b[k:]
	return int(n)
}

func (r *reader) bytes(what string, max int) []byte {
	n := r.count(what, max)
	if r.err != nil {
		return nil
	}
	if n > len(r.b) {
		r.fail(what)
		return nil
	}
	out := bytes.Clone(r.b[:n])
	r.b = r.b[n:]
	return out
}

func (r *reader) str(what string, max int) string { return string(r.bytes(what, max)) }

func (r *reader) byte1(what string) byte {
	if r.err != nil {
		return 0
	}
	if len(r.b) == 0 {
		r.fail(what)
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *reader) u64(what string) uint64 {
	if r.err != nil {
		return 0
	}
	if len(r.b) < 8 {
		r.fail(what)
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

func (r *reader) end(what string) error {
	if r.err == nil && len(r.b) != 0 {
		r.fail(what + ": trailing bytes")
	}
	return r.err
}

func appendBytes(b, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func appendStr(b []byte, s string) []byte { return appendBytes(b, []byte(s)) }

func appendObjs(b []byte, objs []KV) ([]byte, error) {
	if len(objs) > MaxObjects {
		return nil, tooLarge("object list", len(objs), MaxObjects)
	}
	b = binary.AppendUvarint(b, uint64(len(objs)))
	for _, o := range objs {
		if err := checkKV(o); err != nil {
			return nil, err
		}
		b = appendStr(b, o.Key)
		b = appendBytes(b, o.Val)
	}
	return b, nil
}

func readObjs(r *reader) []KV {
	n := r.count("object count", MaxObjects)
	var out []KV
	for i := 0; i < n && r.err == nil; i++ {
		k := r.str("object key", MaxKey)
		v := r.bytes("object value", MaxValue)
		out = append(out, KV{Key: k, Val: v})
	}
	return out
}

func encodeObjs(objs []KV) ([]byte, error) { return appendObjs(nil, objs) }

func decodeObjs(b []byte) ([]KV, error) {
	r := &reader{b: b}
	objs := readObjs(r)
	if err := r.end("objects"); err != nil {
		return nil, err
	}
	return objs, nil
}

const stateVersion = 2

func (st *storeState) encode() ([]byte, error) {
	if len(st.Buckets) > MaxBuckets {
		return nil, tooLarge("bucket list", len(st.Buckets), MaxBuckets)
	}
	if len(st.Ptrs) > MaxBuckets {
		return nil, tooLarge("pointer list", len(st.Ptrs), MaxBuckets)
	}
	b := []byte{stateVersion}
	b = binary.AppendUvarint(b, uint64(len(st.Buckets)))
	for _, bk := range st.Buckets {
		if len(bk.Regs) > MaxRegs {
			return nil, tooLarge("registration list", len(bk.Regs), MaxRegs)
		}
		for _, s := range append([]string{bk.Name, bk.Owner, bk.Sealer}, bk.Regs...) {
			if err := checkName("name", s); err != nil {
				return nil, err
			}
		}
		b = append(b, bk.Status)
		b = appendStr(b, bk.Name)
		b = appendStr(b, bk.Owner)
		b = appendStr(b, bk.Sealer)
		b = binary.AppendUvarint(b, uint64(len(bk.Regs)))
		for _, reg := range bk.Regs {
			b = appendStr(b, reg)
		}
		var err error
		if b, err = appendObjs(b, bk.Objs); err != nil {
			return nil, err
		}
	}
	b = binary.AppendUvarint(b, uint64(len(st.Ptrs)))
	for _, p := range st.Ptrs {
		for _, s := range []string{p.Bucket, p.Loc, p.Writer} {
			if err := checkName("name", s); err != nil {
				return nil, err
			}
		}
		b = appendStr(b, p.Bucket)
		b = appendStr(b, p.Loc)
		b = binary.BigEndian.AppendUint64(b, uint64(p.TS))
		b = appendStr(b, p.Writer)
	}
	return b, nil
}

func decodeState(b []byte) (storeState, error) {
	var st storeState
	r := &reader{b: b}
	if v := r.byte1("state version"); r.err == nil && v != stateVersion {
		r.fail(fmt.Sprintf("state version %d", v))
	}
	nb := r.count("bucket count", MaxBuckets)
	for i := 0; i < nb && r.err == nil; i++ {
		bk := &bucketState{Status: r.byte1("bucket status")}
		bk.Name = r.str("bucket name", MaxName)
		bk.Owner = r.str("bucket owner", MaxName)
		bk.Sealer = r.str("bucket sealer", MaxName)
		nr := r.count("registration count", MaxRegs)
		for j := 0; j < nr && r.err == nil; j++ {
			bk.Regs = append(bk.Regs, r.str("registration", MaxName))
		}
		bk.Objs = readObjs(r)
		st.Buckets = append(st.Buckets, bk)
	}
	np := r.count("pointer count", MaxBuckets)
	for i := 0; i < np && r.err == nil; i++ {
		p := pointerState{Bucket: r.str("pointer bucket", MaxName), Loc: r.str("pointer location", MaxName)}
		p.TS = int64(r.u64("pointer stamp"))
		p.Writer = r.str("pointer writer", MaxName)
		st.Ptrs = append(st.Ptrs, p)
	}
	if err := r.end("state"); err != nil {
		return storeState{}, err
	}
	return st, nil
}
