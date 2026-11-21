package gossip

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrMalformed  = errors.New("gossip: malformed message")
	ErrNotMember  = errors.New("gossip: not a member")
	ErrClosed     = errors.New("gossip: node has left the cluster")
	ErrJoinFailed = errors.New("gossip: join failed")
	ErrViewFull   = errors.New("gossip: membership view is full")
)

const (
	CodeMalformed  = env.CodeAppMin + 16
	CodeNotMember  = env.CodeAppMin + 17
	CodeClosed     = env.CodeAppMin + 18
	CodeJoinFailed = env.CodeAppMin + 19
	CodeViewFull   = env.CodeAppMin + 20
)

func Codes() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeNotMember, Err: ErrNotMember},
		{Code: CodeClosed, Err: ErrClosed},
		{Code: CodeJoinFailed, Err: ErrJoinFailed},
		{Code: CodeViewFull, Err: ErrViewFull},
	}
}

func CodeTable() (*env.CodeTable, error) {
	return env.NewCodeTable(Codes()...)
}
