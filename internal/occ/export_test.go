package occ

func SetCanaryDropConcurrent(on bool) func() {
	old := canaryDropConcurrent
	canaryDropConcurrent = on
	return func() { canaryDropConcurrent = old }
}

func SetHazardReuseEpoch(on bool) func() {
	old := hazardReuseEpoch
	hazardReuseEpoch = on
	return func() { hazardReuseEpoch = old }
}

func EncodeStateBlob(epoch uint64, doc []byte) []byte { return encodeState(epoch, doc) }

func DecodeStateBlob(b []byte) (uint64, *Doc, error) { return decodeState(b) }

func EncodeOpRequest(code byte, key string, p Precondition, value []byte) []byte {
	return encodeOp(op{code: code, key: key, pre: p, value: value})
}

func DecodeResult(b []byte) (Result, error) { return decodeResult(b) }

const (
	MsgDigest = msgDigest
	MsgPush   = msgPush
	MsgOp     = msgOp
	MsgDelta  = msgDelta
	OpGet     = opGet
	OpPut     = opPut
	OpDelete  = opDelete
)
