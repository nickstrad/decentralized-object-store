package node

import (
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func SetCanaryNoDiscardRecount(v bool) { canaryNoDiscardRecount = v }
func SetCanaryIgnoreTombstones(v bool) { canaryIgnoreTombstones = v }

type CollectRecord = collectRecord

func ManifestOfRecord(b []byte) (*storage.Manifest, error) {
	r, err := decodeObject(b)
	if err != nil {
		return nil, err
	}
	return r.Manifest, nil
}

func DecodeRecordsForTest(b []byte) []error {
	_, e1 := decodeObject(b)
	_, e2 := decodeBucket(b)
	_, e3 := decodeStaged(b)
	_, e4 := decodeSet(b)
	return []error{e1, e2, e3, e4}
}

func EncodeSetForTest(ids []string) []byte { return encodeSet(ids) }

func EncodeStagedForTest(saga string, obj []byte) []byte {
	return encodeStaged(stagedRecord{Saga: saga, Object: obj})
}

func NodeRequestForTest(op byte, fields ...string) []byte {
	var w wbuf
	w.byte(chNode)
	w.byte(op)
	for _, f := range fields {
		w.str(f)
	}
	return w.b
}

func (n *Node) MigratorForTest() *migrator { return n.mig }

const (
	OpMetaFetch = opMetaFetch
	OpPointer   = opPointer
	OpPurge     = opPurge
)

func (n *Node) ClaimForTest(c saga.Call, in saga.MigrationInput, done func(error)) {
	n.mig.Claim(c, in, done)
}

func ManifestOfStaged(b []byte) (*storage.Manifest, error) {
	st, err := decodeStaged(b)
	if err != nil {
		return nil, err
	}
	return ManifestOfRecord(st.Object)
}
