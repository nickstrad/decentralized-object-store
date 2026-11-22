package occ

func SetCanaryUnrecordedDiscard(on bool) func() {
	old := canaryUnrecordedDiscard
	canaryUnrecordedDiscard = on
	return func() { canaryUnrecordedDiscard = old }
}

func SetCanaryIncomingLoserSilent(on bool) func() {
	old := canaryIncomingLoserSilent
	canaryIncomingLoserSilent = on
	return func() { canaryIncomingLoserSilent = old }
}

func SetCanaryMergePrefersLocal(on bool) func() {
	old := canaryMergePrefersLocal
	canaryMergePrefersLocal = on
	return func() { canaryMergePrefersLocal = old }
}

func SetHazardReuseEpoch(on bool) func() {
	old := hazardReuseEpoch
	hazardReuseEpoch = on
	return func() { hazardReuseEpoch = old }
}

type State struct {
	Epoch    uint64
	Counter  uint64
	Discards uint64
	Doc      *Doc
}

func DecodeStateBlob(b []byte) (State, error) {
	st, err := decodeState(b)
	return State{Epoch: st.epoch, Counter: st.counter, Discards: st.discards, Doc: st.doc}, err
}

func EncodeStateBlob(epoch, counter, discards uint64, doc []byte) []byte {
	return encodeState(persisted{epoch: epoch, counter: counter, discards: discards}, doc)
}

func DecodeWork(b []byte) (*Doc, int, error) { return decodeDoc(b) }

func (s *Store) Work() int { return s.work }

func EncodeOpRequest(code byte, key string, p Precondition, value []byte) []byte {
	return encodeOp(op{code: code, key: key, pre: p, value: value})
}

func DecodeResult(b []byte) (Result, error) { return decodeResult(b) }

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
