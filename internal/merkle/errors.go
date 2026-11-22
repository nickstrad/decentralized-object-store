package merkle

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors returned by tree operations, wire decoding, diff sessions,
// and anti-entropy repair:
//   - ErrMalformed: a diff request or reply violated the wire format or protocol rules.
//   - ErrDepth: a tree depth or message level was outside [MinDepth, MaxDepth],
//     exceeded the tree depth, or did not match the peer's depth.
//   - ErrKey: a key exceeded MaxKeyLen or appeared twice in Build.
//   - ErrLeafFull: inserting a key would push a leaf past MaxLeafEntries.
//   - ErrRejected: bytes fetched from a peer failed Repairer.Valid.
//   - ErrSession: Session.Feed was called when no request was awaiting a reply.
var (
	ErrMalformed = errors.New("merkle: malformed message")
	ErrDepth     = errors.New("merkle: depth out of range")
	ErrKey       = errors.New("merkle: invalid key")
	ErrLeafFull  = errors.New("merkle: leaf full")
	ErrRejected  = errors.New("merkle: fetched bytes rejected")
	ErrSession   = errors.New("merkle: session not waiting for a reply")
)

// Wire status codes for merkle errors transmitted across an env.Transport.
// Both codes sit in the package's reserved block 112..127.
const (
	CodeMalformed = env.CodeAppMin + 48
	CodeDepth     = env.CodeAppMin + 49
)

// CodeEntries returns the wire-code registrations for this package's remote
// errors, ready to pass to env.NewCodeTable.
func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeDepth, Err: ErrDepth},
	}
}
