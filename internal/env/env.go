// Package env defines the narrow environment interfaces that single-threaded
// protocol cores use for time, randomness, networking, and durable storage.
//
// Protocol packages depend only on this package so the same state machine runs
// unchanged inside the deterministic simulator and on the real runtime. Calls
// into a Process are serialized by its host, and callbacks never run
// re-entrantly inside the call that scheduled them.
package env

import (
	"math/rand/v2"
	"strconv"
	"time"
)

// NodeID uniquely identifies a node in a cluster.
type NodeID string

// Region names a failure domain or geographic placement zone for a node.
type Region string

// Time is an instant in virtual nanoseconds from a fixed epoch.
type Time int64

// Add returns t advanced by d.
func (t Time) Add(d time.Duration) Time { return t + Time(d) }

// Sub returns the elapsed duration t - u.
func (t Time) Sub(u Time) time.Duration { return time.Duration(t - u) }

// String formats t as a decimal nanosecond count with an "ns" suffix.
func (t Time) String() string {
	return strconv.FormatInt(int64(t), 10) + "ns"
}

// Clock provides a node's local time and single-shot timers.
// Callbacks scheduled with AfterFunc run on the node's event loop, never
// synchronously inside AfterFunc.
type Clock interface {
	// Now returns the node's current local time, including any configured skew
	// and drift.
	Now() Time
	// AfterFunc schedules fn to run after d of local time and returns a Timer
	// that can stop or reset it. A non-positive d fires at the current instant
	// after already-queued events.
	AfterFunc(d time.Duration, fn func()) Timer
}

// Timer controls a pending callback scheduled through Clock.AfterFunc.
type Timer interface {
	// Stop prevents the timer from firing and reports whether it was pending.
	Stop() bool
	// Reset schedules the timer to fire after d of local time and reports
	// whether it was pending before the reset. A fired or stopped timer may be
	// reset to arm it again.
	Reset(d time.Duration) bool
}

// Transport sends unreliable datagrams and ordered request/response exchanges.
// Messages cross as byte slices; callers must not rely on shared memory across
// nodes.
type Transport interface {
	// Send transmits a best-effort datagram to peer to. It returns ErrClosed,
	// ErrTooLarge, or ErrUnreachable for immediate local failures; network
	// drops, partitions, duplicates, and reordering occur silently.
	Send(to NodeID, msg []byte) error
	// Request sends req to peer to and invokes cb once with either the response
	// or an error (ErrTimeout, ErrUnreachable, ErrTooLarge, ErrInvalidArgument,
	// or a *RemoteError). If the caller's incarnation is closed before
	// completion, cb is not called.
	Request(to NodeID, req []byte, timeout time.Duration, cb func(reply []byte, err error))
}

// Disk is a synchronous per-node store of named byte blobs with separate
// written and durable layers. Reads observe the written layer; Sync makes a
// blob's written state (including a delete) durable across crashes.
type Disk interface {
	// Read returns a copy of the blob's written bytes, or ErrNotFound, ErrIO,
	// ErrInvalidArgument, or ErrClosed.
	Read(name string) ([]byte, error)
	// Write replaces the blob's written layer with a copy of data.
	Write(name string, data []byte) error
	// Append extends the blob's written layer with data, creating the blob if
	// it does not exist.
	Append(name string, data []byte) error
	// Sync commits the blob's current written state or pending deletion to the
	// durable layer.
	Sync(name string) error
	// Delete removes the blob from the written layer; Sync makes the deletion
	// durable.
	Delete(name string) error
	// List returns the sorted names of blobs in the written layer whose names
	// start with prefix.
	List(prefix string) ([]string, error)
}

// Reply completes an inbound request passed to Process.OnRequest.
// It may be called synchronously inside OnRequest or later from another event,
// and must be called at most once.
type Reply func(resp []byte, err error)

// Process is the single-threaded state machine hosted by one node incarnation.
type Process interface {
	// Start runs once when the node incarnation comes up, before any inbound
	// messages are delivered to it.
	Start()
	// OnDatagram handles a datagram sent by from.
	OnDatagram(from NodeID, msg []byte)
	// OnRequest handles a request from peer from; the implementation completes
	// it by calling reply.
	OnRequest(from NodeID, req []byte, reply Reply)
	// EncodeState returns a canonical byte representation of the process's
	// volatile state for state-digest comparison. It must be deterministic.
	EncodeState() []byte
}

// Env bundles the identity and dependencies supplied to a Process on startup.
// The simulator creates a fresh Env for each node incarnation.
type Env struct {
	ID     NodeID
	Region Region
	Clock  Clock
	Rand   *rand.Rand
	Net    Transport
	Disk   Disk
}

// WithNet returns a copy of e with Net replaced by t.
func (e Env) WithNet(t Transport) Env {
	e.Net = t
	return e
}

// WithDisk returns a copy of e with Disk replaced by d.
func (e Env) WithDisk(d Disk) Env {
	e.Disk = d
	return e
}

// WithClock returns a copy of e with Clock replaced by c.
func (e Env) WithClock(c Clock) Env {
	e.Clock = c
	return e
}
