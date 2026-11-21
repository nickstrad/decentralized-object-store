package occ

import (
	"crypto/sha256"
	"fmt"
)

const (
	msgDigest byte = 1
	msgPush   byte = 2
	msgOp     byte = 3
	msgDelta  byte = 4

	replyMatch byte = 1
	replyState byte = 2
	replyAck   byte = 3

	opGet    byte = 1
	opPut    byte = 2
	opDelete byte = 3
)

type op struct {
	code  byte
	key   string
	pre   Precondition
	value []byte
}

func encodeOp(o op) []byte {
	e := encoder{b: []byte{msgOp, o.code}}
	e.str(o.key)
	e.byte(byte(o.pre.kind))
	if o.pre.kind == preIfMatch {
		e.raw(o.pre.etag[:])
	}
	e.bytes(o.value)
	return e.b
}

func decodeOp(b []byte) (op, error) {
	d := &decoder{b: b}
	if d.byte() != msgOp {
		d.fail("op message")
	}
	o := op{code: d.byte()}
	if d.err == nil && (o.code < opGet || o.code > opDelete) {
		d.fail("op code")
	}
	o.key = d.str()
	o.pre.kind = preKind(d.byte())
	switch o.pre.kind {
	case preIfMatch:
		d.fixed((*[32]byte)(&o.pre.etag))
	case preNone, preIfNoneMatch:
	default:
		d.fail("precondition")
	}
	o.value = d.bytes()
	d.end()
	if d.err != nil {
		return op{}, d.err
	}
	if o.code != opPut && len(o.value) > 0 {
		return op{}, fmt.Errorf("occ: decode op: value on a non-put: %w", ErrMalformed)
	}
	o.value = append([]byte(nil), o.value...)
	return o, nil
}

type Result struct {
	Version VersionID
	ETag    ETag
	Value   []byte
}

func encodeResult(r Result) []byte {
	var e encoder
	e.fixed((*[32]byte)(&r.Version))
	e.fixed((*[32]byte)(&r.ETag))
	e.bytes(r.Value)
	return e.b
}

func decodeResult(b []byte) (Result, error) {
	d := &decoder{b: b}
	var r Result
	d.fixed((*[32]byte)(&r.Version))
	d.fixed((*[32]byte)(&r.ETag))
	r.Value = append([]byte(nil), d.bytes()...)
	d.end()
	if d.err != nil {
		return Result{}, d.err
	}
	return r, nil
}

func digestRequest(d [32]byte) []byte {
	return append([]byte{msgDigest}, d[:]...)
}

func stateDigest(state []byte) [32]byte { return sha256.Sum256(state) }
