package merkle

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrMalformed = errors.New("merkle: malformed message")
	ErrDepth     = errors.New("merkle: depth out of range")
	ErrKey       = errors.New("merkle: invalid key")
	ErrLeafFull  = errors.New("merkle: leaf full")
	ErrRejected  = errors.New("merkle: fetched bytes rejected")
	ErrSession   = errors.New("merkle: session not waiting for a reply")
)

const (
	CodeMalformed = env.CodeAppMin + 48
	CodeDepth     = env.CodeAppMin + 49
)

func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeMalformed, Err: ErrMalformed},
		{Code: CodeDepth, Err: ErrDepth},
	}
}
