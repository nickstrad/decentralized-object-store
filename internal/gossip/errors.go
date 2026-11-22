package gossip

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors returned by Node operations and wire decoding:
//   - ErrMalformed: a datagram or request failed framing, size, kind, or field checks.
//   - ErrNotMember: Lookup named an ID not currently held in the node's view.
//   - ErrClosed: an operation targeted a node that has already called Leave.
//   - ErrJoinFailed: every seed contacted by Join failed; individual causes are joined.
//   - ErrViewFull: a new member or tombstone reinstatement was refused at Config.MaxMembers.
var (
	ErrMalformed  = errors.New("gossip: malformed message")
	ErrNotMember  = errors.New("gossip: not a member")
	ErrClosed     = errors.New("gossip: node has left the cluster")
	ErrJoinFailed = errors.New("gossip: join failed")
	ErrViewFull   = errors.New("gossip: membership view is full")
)

// Wire status codes mapping gossip sentinels into the reserved gossip block
// (CodeAppMin+16..CodeAppMin+20, i.e. 80..84).
const (
	CodeMalformed  = env.CodeAppMin + 16
	CodeNotMember  = env.CodeAppMin + 17
	CodeClosed     = env.CodeAppMin + 18
	CodeJoinFailed = env.CodeAppMin + 19
	CodeViewFull   = env.CodeAppMin + 20
)

// Codes returns the wire code entries for all gossip sentinel errors.
func Codes() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeNotMember, Err: ErrNotMember},
		{Code: CodeClosed, Err: ErrClosed},
		{Code: CodeJoinFailed, Err: ErrJoinFailed},
		{Code: CodeViewFull, Err: ErrViewFull},
	}
}

// CodeTable builds an env.CodeTable containing gossip's wire codes alongside
// the built-in env sentinels.
func CodeTable() (*env.CodeTable, error) {
	return env.NewCodeTable(Codes()...)
}
