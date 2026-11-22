package node

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var (
	ErrNotReady     = errors.New("node: not ready")
	ErrStale        = errors.New("node: fetched version lost to the local one")
	ErrUnsettled    = errors.New("node: replicas disagree")
	ErrFenced       = errors.New("node: operation fenced")
	ErrMalformed    = errors.New("node: malformed message")
	ErrUnknownSaga  = errors.New("node: migration not coordinated here")
	ErrFrame        = errors.New("node: malformed frame")
	ErrStopped      = errors.New("node: stopped")
	ErrNoMigration  = errors.New("node: bucket cannot migrate")
	ErrConfig       = errors.New("node: invalid configuration")
	ErrStaleRequest = errors.New("node: request superseded")
)

const (
	CodeNotReady     = env.CodeAppMin + 112
	CodeStale        = env.CodeAppMin + 113
	CodeUnsettled    = env.CodeAppMin + 114
	CodeFenced       = env.CodeAppMin + 115
	CodeMalformed    = env.CodeAppMin + 116
	CodeUnknownSaga  = env.CodeAppMin + 117
	CodeFrame        = env.CodeAppMin + 118
	CodeStopped      = env.CodeAppMin + 119
	CodeNoMigration  = env.CodeAppMin + 120
	CodeConfig       = env.CodeAppMin + 121
	CodeStaleRequest = env.CodeAppMin + 122
)

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
		{Code: CodeStaleRequest, Err: ErrStaleRequest},
	}
}

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

func CodeTable() (*env.CodeTable, error) {
	return env.NewCodeTable(AllCodeEntries()...)
}
