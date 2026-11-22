package sagasim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
)

// Per-RPC request timeouts used by Ops. CopyTimeout is longer because opCopy
// relays an opDump request to the source store.
const (
	RPCTimeout  = 500 * time.Millisecond
	CopyTimeout = 1500 * time.Millisecond
)

// Ops implements saga.MigrationOps against a set of simulated Store nodes over
// env.Transport. Ownership and registration checks are keyed by Call.Key.Saga.
type Ops struct {
	e      env.Env
	stores []env.NodeID
}

// NewOps constructs an Ops client targeting the given store node IDs.
func NewOps(e env.Env, stores []env.NodeID) *Ops {
	s := slices.Clone(stores)
	slices.Sort(s)
	return &Ops{e: e, stores: s}
}

func (o *Ops) known(loc string) bool { return slices.Contains(o.stores, env.NodeID(loc)) }

func (o *Ops) call(to string, timeout time.Duration, msg []byte, cb func([]byte, error)) {
	if !o.known(to) {
		cb(nil, permanent("unknown location %q", to))
		return
	}
	o.e.Net.Request(env.NodeID(to), msg, timeout, cb)
}

func (o *Ops) unit(to string, msg []byte, done func(error)) {
	if !o.known(to) {
		done(nil)
		return
	}
	o.call(to, RPCTimeout, msg, func(_ []byte, err error) { done(err) })
}

func sagaField(c saga.Call) []byte { return []byte(c.Key.Saga) }

// Claim registers the saga at m.From and claims staging ownership at m.To.
func (o *Ops) Claim(c saga.Call, m saga.MigrationInput, done func(error)) {
	if !o.known(m.From) || !o.known(m.To) {
		done(permanent("claim %s: unknown location %q or %q", m.Bucket, m.From, m.To))
		return
	}
	o.call(m.From, RPCTimeout, encodeFields(opRegister, []byte(m.Bucket), sagaField(c)), func(_ []byte, err error) {
		if err != nil {
			done(err)
			return
		}
		o.unit(m.To, encodeFields(opClaim, []byte(m.Bucket), sagaField(c)), done)
	})
}

// Unclaim releases the staging claim at m.To and then deregisters at m.From.
func (o *Ops) Unclaim(c saga.Call, m saga.MigrationInput, done func(error)) {
	o.unit(m.To, encodeFields(opRelease, []byte(m.Bucket), sagaField(c)), func(err error) {
		if err != nil {
			done(err)
			return
		}
		o.unit(m.From, encodeFields(opDeregister, []byte(m.Bucket), sagaField(c)), done)
	})
}

// Copy asks m.To to pull bucket objects from m.From and returns the copied root.
func (o *Ops) Copy(c saga.Call, m saga.MigrationInput, done func([]byte, error)) {
	o.call(m.To, CopyTimeout, encodeFields(opCopy, []byte(m.Bucket), sagaField(c), []byte(m.From)), done)
}

// Discard removes staged objects for m.Bucket at m.To.
func (o *Ops) Discard(c saga.Call, m saga.MigrationInput, done func(error)) {
	o.unit(m.To, encodeFields(opDiscard, []byte(m.Bucket), sagaField(c)), done)
}

// Root fetches the bucket's current Merkle root from store loc.
func (o *Ops) Root(c saga.Call, bucket, loc string, done func([]byte, error)) {
	o.call(loc, RPCTimeout, encodeFields(opRoot, []byte(bucket)), done)
}

// fanout sends msg to every store replica concurrently and invokes done once
// all replies or timeouts have arrived.
func (o *Ops) fanout(msg []byte, done func([][]byte, error)) {
	replies := make([][]byte, len(o.stores))
	errs := make([]error, len(o.stores))
	left := len(o.stores)
	for i, id := range o.stores {
		o.e.Net.Request(id, msg, RPCTimeout, func(resp []byte, err error) {
			replies[i] = resp
			if err != nil {
				errs[i] = fmt.Errorf("sagasim: replica %s: %w", id, err)
			}
			left--
			if left == 0 {
				done(replies, errors.Join(errs...))
			}
		})
	}
}

// Flip writes the routing pointer for m.Bucket to every store replica using LWW
// stamp (c.Begun, c.Key.Saga). It succeeds only when every replica acknowledges.
func (o *Ops) Flip(c saga.Call, m saga.MigrationInput, done func(error)) {
	stamp := binary.BigEndian.AppendUint64(nil, uint64(c.Begun))
	o.fanout(encodeFields(opFlip, []byte(m.Bucket), []byte(m.To), stamp, sagaField(c)), func(_ [][]byte, err error) { done(err) })
}

// Pointer reads the routing pointer from every store replica and returns the
// location only if all replicas reply and agree; disagreement returns ErrUnsettled.
func (o *Ops) Pointer(c saga.Call, bucket string, done func(string, error)) {
	o.fanout(encodeFields(opPointer, []byte(bucket)), func(replies [][]byte, err error) {
		if err != nil {
			done("", err)
			return
		}
		var first []byte
		for i, r := range replies {
			_, f, derr := decodeFields(r)
			if derr != nil || len(f) != 3 {
				done("", fmt.Errorf("sagasim: pointer reply from %s: %w", o.stores[i], ErrMalformed))
				return
			}
			if i == 0 {
				first = f[0]
			} else if !bytes.Equal(first, f[0]) {
				done("", fmt.Errorf("sagasim: pointer of %s: %q and %q: %w", bucket, first, f[0], ErrUnsettled))
				return
			}
		}
		done(string(first), nil)
	})
}

// Seal freezes m.From against new writes once this saga is the sole registrant,
// returning retired=true if this saga has already retired m.From.
func (o *Ops) Seal(c saga.Call, m saga.MigrationInput, done func(bool, error)) {
	o.call(m.From, RPCTimeout, encodeFields(opSeal, []byte(m.Bucket), sagaField(c)), func(resp []byte, err error) {
		if err != nil {
			done(false, err)
			return
		}
		done(len(resp) == 1 && resp[0] == 1, nil)
	})
}

// Promote transitions the staged copy at m.To to live.
func (o *Ops) Promote(c saga.Call, m saga.MigrationInput, done func(error)) {
	o.call(m.To, RPCTimeout, encodeFields(opPromote, []byte(m.Bucket), sagaField(c)), func(_ []byte, err error) { done(err) })
}

// Retire clears the sealed source bucket data and registrations at m.From.
func (o *Ops) Retire(c saga.Call, m saga.MigrationInput, done func(error)) {
	o.call(m.From, RPCTimeout, encodeFields(opRetire, []byte(m.Bucket), sagaField(c)), func(_ []byte, err error) { done(err) })
}
