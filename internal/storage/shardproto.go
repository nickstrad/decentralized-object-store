package storage

import (
	"encoding/binary"
	"fmt"
)

// Op identifies a shard RPC operation on the wire.
type Op uint8

// Wire operation codes for shard requests.
const (
	OpPutShard    Op = 1
	OpGetShard    Op = 2
	OpDeleteShard Op = 3
)

// EncodeShardRequest serializes a shard RPC request as op byte, uvarint-length
// canonical blob name, and body (the shard blob for OpPutShard; empty for
// OpGetShard and OpDeleteShard).
func EncodeShardRequest(op Op, id ShardID, body []byte) []byte {
	name := id.BlobName()
	b := make([]byte, 0, 1+binary.MaxVarintLen64+len(name)+len(body))
	b = append(b, byte(op))
	b = appendString(b, name)
	return append(b, body...)
}

// DecodeShardRequest parses a wire shard request and returns the operation,
// target ShardID, and a body subslice aliasing req. It returns ErrInvalid on
// empty input, unknown op, non-canonical blob name, or a non-empty body on
// OpGetShard or OpDeleteShard.
func DecodeShardRequest(req []byte) (Op, ShardID, []byte, error) {
	bad := func(why string) (Op, ShardID, []byte, error) {
		return 0, ShardID{}, nil, fmt.Errorf("storage: shard request: %s: %w", why, ErrInvalid)
	}
	if len(req) == 0 {
		return bad("empty")
	}
	op := Op(req[0])
	if op != OpPutShard && op != OpGetShard && op != OpDeleteShard {
		return bad(fmt.Sprintf("unknown op %d", op))
	}
	r := &reader{b: req[1:]}
	name := r.str(uint64(len(req)))
	if r.err != nil {
		return bad(r.err.Error())
	}
	id, err := ParseBlobName(name)
	if err != nil {
		return 0, ShardID{}, nil, fmt.Errorf("storage: shard request: %w", err)
	}
	if op != OpPutShard && len(r.b) != 0 {
		return bad("unexpected body")
	}
	return op, id, r.b, nil
}
