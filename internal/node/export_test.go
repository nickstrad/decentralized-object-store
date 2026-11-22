package node

import (
	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// Canary switches exposed to black-box tests for planting missed recounts or
// tombstone suppression.
func SetCanaryNoDiscardRecount(v bool) { canaryNoDiscardRecount = v }
func SetCanaryIgnoreTombstones(v bool) { canaryIgnoreTombstones = v }

// CollectRecord re-exports collectRecord for black-box tests.
type CollectRecord = collectRecord

// DecodeRecordsForTest runs all four record decoders on b and returns their
// errors for malformed-input tests.
func DecodeRecordsForTest(b []byte) []error {
	_, e1 := decodeObject(b)
	_, e2 := decodeBucket(b)
	_, e3 := decodeStaged(b)
	_, e4 := decodePointerView(b)
	return []error{e1, e2, e3, e4}
}

// EncodeStagedForTest builds a stagedRecord encoding for tests.
func EncodeStagedForTest(saga string, obj []byte) []byte {
	return encodeStaged(stagedRecord{Saga: saga, Object: obj})
}

// NodeRequestForTest builds a chNode RPC payload with opcode and string fields.
func NodeRequestForTest(op byte, fields ...string) []byte {
	var w wbuf
	w.byte(chNode)
	w.byte(op)
	for _, f := range fields {
		w.str(f)
	}
	return w.b
}

// MigratorForTest returns the node's internal migrator for unit tests.
func (n *Node) MigratorForTest() *migrator { return n.mig }

// Exported chNode opcodes for unit tests.
const (
	OpMetaFetch = opMetaFetch
	OpPointer   = opPointer
	OpPurge     = opPurge
)

// ClaimForTest invokes migrator.Claim directly for unit tests.
func (n *Node) ClaimForTest(c saga.Call, in saga.MigrationInput, done func(error)) {
	n.mig.Claim(c, in, done)
}

// EncodePointerViewForTest encodes a pointerView with zero digest for tests.
func EncodePointerViewForTest(route, owner string) []byte {
	return pointerView{route: route, owner: owner}.encode()
}

// CopyForTest invokes migrator.Copy directly for unit tests.
func (n *Node) CopyForTest(c saga.Call, in saga.MigrationInput, done func([]byte, error)) {
	n.mig.Copy(c, in, done)
}

// SetCanaryNoQuotaAudit disables auditQuotas for the quota-audit canary test.
func SetCanaryNoQuotaAudit(v bool) { canaryNoQuotaAudit = v }

// PointerForTest invokes migrator.Pointer directly for unit tests.
func (n *Node) PointerForTest(c saga.Call, bucket string, done func(string, error)) {
	n.mig.Pointer(c, bucket, done)
}

// FlipKeyForTest returns the bucket's ~flip OCC tag key.
func FlipKeyForTest(bucket string) string { return bucketKey(bucket, tagFlip) }

// AbsentKeyForTest returns the bucket-scoped ~absent OCC tag key for id/holder.
func AbsentKeyForTest(id storage.ShardID, holder env.NodeID) string {
	return absentKey(id.Bucket, id, holder)
}
