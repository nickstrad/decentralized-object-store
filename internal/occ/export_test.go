package occ

// SetCanaryUnrecordedDiscard toggles the canary switch that suppresses LWW
// discard recording and returns a function that restores the previous setting.
func SetCanaryUnrecordedDiscard(on bool) func() {
	old := canaryUnrecordedDiscard
	canaryUnrecordedDiscard = on
	return func() { canaryUnrecordedDiscard = old }
}

// SetCanaryIncomingLoserSilent toggles the canary switch that drops rejected
// incoming concurrent losers without recording them and returns a restore hook.
func SetCanaryIncomingLoserSilent(on bool) func() {
	old := canaryIncomingLoserSilent
	canaryIncomingLoserSilent = on
	return func() { canaryIncomingLoserSilent = old }
}

// SetCanaryMergePrefersLocal toggles the canary switch that makes Doc merges
// keep the locally held version and returns a function that restores it.
func SetCanaryMergePrefersLocal(on bool) func() {
	old := canaryMergePrefersLocal
	canaryMergePrefersLocal = on
	return func() { canaryMergePrefersLocal = old }
}

// SetHazardReuseEpoch toggles the test switch that forces freshly opened nodes
// to reuse epoch 1 instead of drawing a random epoch, returning a restore hook.
func SetHazardReuseEpoch(on bool) func() {
	old := hazardReuseEpoch
	hazardReuseEpoch = on
	return func() { hazardReuseEpoch = old }
}

// State exposes decoded fields of a local state blob to black-box tests.
type State struct {
	Epoch    uint64
	Counter  uint64
	Discards uint64
	Doc      *Doc
}

// DecodeStateBlob decodes an OCS2 local state blob for black-box test assertions.
func DecodeStateBlob(b []byte) (State, error) {
	st, err := decodeState(b)
	return State{Epoch: st.epoch, Counter: st.counter, Discards: st.discards, Doc: st.doc}, err
}

// EncodeStateBlob encodes an OCS2 local state blob with empty tail, outbox,
// and dedup window for golden and malformed-input tests.
func EncodeStateBlob(epoch, counter, discards uint64, doc []byte) []byte {
	return encodeState(persisted{epoch: epoch, counter: counter, discards: discards}, doc)
}

// DecodeWork decodes a Doc and returns the decoder's counted operation total.
func DecodeWork(b []byte) (*Doc, int, error) { return decodeDoc(b) }

// Work returns the cumulative classification operation count performed by s.
func (s *Store) Work() int { return s.work }

// EncodeOpRequest encodes a client operation wire frame for protocol tests.
func EncodeOpRequest(code byte, key string, p Precondition, value []byte) []byte {
	return encodeOp(op{code: code, key: key, pre: p, value: value})
}

// DecodeResult decodes a client operation wire reply for protocol tests.
func DecodeResult(b []byte) (Result, error) { return decodeResult(b) }

// Wire message and operation tag constants exported to black-box protocol tests.
const (
	MsgDigest  = msgDigest
	MsgPush    = msgPush
	MsgOp      = msgOp
	MsgDelta   = msgDelta
	ReplyState = replyState
	OpGet      = opGet
	OpPut      = opPut
	OpDelete   = opDelete
)
