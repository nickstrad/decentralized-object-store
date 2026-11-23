package node

import (
	"errors"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/storage"
)

// Sentinel errors returned by Node and its wire protocol:
//   - ErrNotReady: Start has not finished loading metadata yet.
//   - ErrStale: a fetched metadata record lost to the local version under LWW.
//   - ErrUnsettled: peers disagree on a migration pointer view or membership is still settling.
//   - ErrFenced: a discarded migration copy or a shard with a binding absence record was refused.
//   - ErrMalformed: an inbound wire message, record, or disallowed shard-delete RPC was invalid.
//   - ErrUnknownSaga: Migration queried a saga ID not coordinated by this node.
//   - ErrFrame: a transport frame or datagram header could not be decoded.
//   - ErrStopped: an operation or RPC arrived after Node.Stop.
//   - ErrNoMigration: the bucket is unrouted or not routed to the expected region.
//   - ErrConfig: Config or RuntimeConfig failed validation.
var (
	ErrNotReady    = errors.New("node: not ready")
	ErrStale       = errors.New("node: fetched version lost to the local one")
	ErrUnsettled   = errors.New("node: replicas disagree")
	ErrFenced      = errors.New("node: operation fenced")
	ErrMalformed   = errors.New("node: malformed message")
	ErrUnknownSaga = errors.New("node: migration not coordinated here")
	ErrFrame       = errors.New("node: malformed frame")
	ErrStopped     = errors.New("node: stopped")
	ErrNoMigration = errors.New("node: bucket cannot migrate")
	ErrConfig      = errors.New("node: invalid configuration")
)

// Wire error codes for node sentinels (block 176–185).
const (
	CodeNotReady    = env.CodeAppMin + 112
	CodeStale       = env.CodeAppMin + 113
	CodeUnsettled   = env.CodeAppMin + 114
	CodeFenced      = env.CodeAppMin + 115
	CodeMalformed   = env.CodeAppMin + 116
	CodeUnknownSaga = env.CodeAppMin + 117
	CodeFrame       = env.CodeAppMin + 118
	CodeStopped     = env.CodeAppMin + 119
	CodeNoMigration = env.CodeAppMin + 120
	CodeConfig      = env.CodeAppMin + 121
)

// NodeCodeEntries returns the env.CodeEntry rows for this package's sentinels.
func NodeCodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeNotReady, Err: ErrNotReady},
		{Code: CodeStale, Err: ErrStale},
		{Code: CodeUnsettled, Err: ErrUnsettled},
		{Code: CodeFenced, Err: ErrFenced},
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeUnknownSaga, Err: ErrUnknownSaga},
		{Code: CodeFrame, Err: ErrFrame},
		{Code: CodeStopped, Err: ErrStopped},
		{Code: CodeNoMigration, Err: ErrNoMigration},
		{Code: CodeConfig, Err: ErrConfig},
	}
}

// AllCodeEntries joins the wire error entries of gossip, storage, merkle, occ,
// saga, the object API, and node into one slice.
func AllCodeEntries() []env.CodeEntry {
	var all []env.CodeEntry
	all = append(all, gossip.Codes()...)
	all = append(all, storage.CodeEntries()...)
	all = append(all, merkle.CodeEntries()...)
	all = append(all, occ.CodeEntries()...)
	all = append(all, saga.Codes()...)
	all = append(all, storage.APICodeEntries()...)
	all = append(all, NodeCodeEntries()...)
	return all
}

// CodeTable builds the unified env.CodeTable used by both the simulator and the
// real runtime. It fails if any two packages share a wire code.
func CodeTable() (*env.CodeTable, error) {
	return env.NewCodeTable(AllCodeEntries()...)
}
