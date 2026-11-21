package sagasim

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
)

var (
	ErrBusy      = errors.New("sagasim: bucket busy")
	ErrMoved     = errors.New("sagasim: bucket not writable here")
	ErrUnsettled = errors.New("sagasim: pointer replicas disagree")
	ErrCheck     = errors.New("sagasim: check failed")
	ErrMalformed = errors.New("sagasim: malformed message")
)

const (
	CodeBusy      env.Code = env.CodeAppMin + 56
	CodeMoved     env.Code = env.CodeAppMin + 57
	CodeUnsettled env.Code = env.CodeAppMin + 58
	CodeMalformed env.Code = env.CodeAppMin + 59
)

func Codes() []env.CodeEntry {
	return append(saga.Codes(),
		env.CodeEntry{Code: CodeBusy, Err: ErrBusy},
		env.CodeEntry{Code: CodeMoved, Err: ErrMoved},
		env.CodeEntry{Code: CodeUnsettled, Err: ErrUnsettled},
		env.CodeEntry{Code: CodeMalformed, Err: ErrMalformed},
	)
}

const (
	stateBlob    = "store"
	loadRetry    = 50 * time.Millisecond
	dumpTimeout  = 600 * time.Millisecond
	maxFields    = 64
	maxFieldSize = 1 << 20
)

const (
	statusLive uint8 = iota + 1
	statusStaging
	statusSealed
	statusRetired
)

const (
	opRegister byte = iota + 1
	opDeregister
	opClaim
	opRelease
	opCopy
	opDump
	opDiscard
	opRoot
	opFlip
	opPointer
	opSeal
	opPromote
	opRetire
	opPut
)

type KV struct {
	Key string
	Val []byte
}

type bucketState struct {
	Name   string
	Status uint8
	Owner  string
	Sealer string
	Regs   []string
	Objs   []KV
}

type pointerState struct {
	Bucket string
	Loc    string
	TS     int64
	Writer string
}

type storeState struct {
	Buckets []*bucketState
	Ptrs    []pointerState
}

func encodeFields(op byte, fields ...[]byte) []byte {
	b := []byte{op}
	b = binary.AppendUvarint(b, uint64(len(fields)))
	for _, f := range fields {
		b = binary.AppendUvarint(b, uint64(len(f)))
		b = append(b, f...)
	}
	return b
}

func decodeFields(b []byte) (byte, [][]byte, error) {
	if len(b) == 0 {
		return 0, nil, fmt.Errorf("sagasim: empty message: %w", ErrMalformed)
	}
	op, rest := b[0], b[1:]
	n, k := binary.Uvarint(rest)
	if k <= 0 || n > maxFields {
		return 0, nil, fmt.Errorf("sagasim: field count: %w", ErrMalformed)
	}
	rest = rest[k:]
	out := make([][]byte, 0, n)
	for i := uint64(0); i < n; i++ {
		l, k := binary.Uvarint(rest)
		if k <= 0 || l > maxFieldSize || l > uint64(len(rest)-k) {
			return 0, nil, fmt.Errorf("sagasim: field %d: %w", i, ErrMalformed)
		}
		out = append(out, rest[k:k+int(l)])
		rest = rest[k+int(l):]
	}
	if len(rest) != 0 {
		return 0, nil, fmt.Errorf("sagasim: trailing bytes: %w", ErrMalformed)
	}
	return op, out, nil
}

func encodeObjs(objs []KV) []byte {
	var fields [][]byte
	for _, o := range objs {
		fields = append(fields, []byte(o.Key), o.Val)
	}
	return encodeFields(0, fields...)
}

func decodeObjs(b []byte) ([]KV, error) {
	_, f, err := decodeFields(b)
	if err != nil || len(f)%2 != 0 {
		return nil, fmt.Errorf("sagasim: objects: %w", ErrMalformed)
	}
	var out []KV
	for i := 0; i < len(f); i += 2 {
		out = append(out, KV{Key: string(f[i]), Val: bytes.Clone(f[i+1])})
	}
	return out, nil
}

func Root(objs []KV) []byte {
	h := sha256.New()
	for _, o := range objs {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(o.Key)))])
		h.Write([]byte(o.Key))
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(o.Val)))])
		h.Write(o.Val)
	}
	return h.Sum(nil)
}

func (st *storeState) encode() []byte {
	var fields [][]byte
	for _, bk := range st.Buckets {
		fields = append(fields, encodeFields(bk.Status, []byte(bk.Name), []byte(bk.Owner), []byte(bk.Sealer),
			[]byte(strings.Join(bk.Regs, "\x00")), encodeObjs(bk.Objs)))
	}
	var ptrs [][]byte
	for _, p := range st.Ptrs {
		ptrs = append(ptrs, encodeFields(0, []byte(p.Bucket), []byte(p.Loc), binary.BigEndian.AppendUint64(nil, uint64(p.TS)), []byte(p.Writer)))
	}
	return encodeFields(1, encodeFields(0, fields...), encodeFields(0, ptrs...))
}

func decodeState(b []byte) (storeState, error) {
	var st storeState
	_, top, err := decodeFields(b)
	if err != nil || len(top) != 2 {
		return st, fmt.Errorf("sagasim: state: %w", ErrMalformed)
	}
	_, bks, err := decodeFields(top[0])
	if err != nil {
		return st, err
	}
	for _, raw := range bks {
		status, f, err := decodeFields(raw)
		if err != nil || len(f) != 5 {
			return st, fmt.Errorf("sagasim: bucket: %w", ErrMalformed)
		}
		objs, err := decodeObjs(f[4])
		if err != nil {
			return st, err
		}
		bk := &bucketState{Name: string(f[0]), Status: status, Owner: string(f[1]), Sealer: string(f[2]), Objs: objs}
		if len(f[3]) > 0 {
			bk.Regs = strings.Split(string(f[3]), "\x00")
		}
		st.Buckets = append(st.Buckets, bk)
	}
	_, ps, err := decodeFields(top[1])
	if err != nil {
		return st, err
	}
	for _, raw := range ps {
		_, f, err := decodeFields(raw)
		if err != nil || len(f) != 4 || len(f[2]) != 8 {
			return st, fmt.Errorf("sagasim: pointer: %w", ErrMalformed)
		}
		st.Ptrs = append(st.Ptrs, pointerState{Bucket: string(f[0]), Loc: string(f[1]), TS: int64(binary.BigEndian.Uint64(f[2])), Writer: string(f[3])})
	}
	return st, nil
}

func (st *storeState) bucket(name string) *bucketState {
	for _, b := range st.Buckets {
		if b.Name == name {
			return b
		}
	}
	return nil
}

func (st *storeState) put(b *bucketState) {
	for i, x := range st.Buckets {
		if x.Name == b.Name {
			st.Buckets[i] = b
			return
		}
	}
	st.Buckets = append(st.Buckets, b)
	slices.SortFunc(st.Buckets, func(x, y *bucketState) int { return strings.Compare(x.Name, y.Name) })
}

func (st *storeState) drop(name string) {
	st.Buckets = slices.DeleteFunc(st.Buckets, func(b *bucketState) bool { return b.Name == name })
}

func (st *storeState) pointer(bucket string) (pointerState, int) {
	for i, p := range st.Ptrs {
		if p.Bucket == bucket {
			return p, i
		}
	}
	return pointerState{Bucket: bucket}, -1
}

type BucketSeed struct {
	Name string
	Loc  env.NodeID
	Objs []KV
}

type Store struct {
	e        env.Env
	seeds    []BucketSeed
	st       storeState
	loaded   bool
	loadErrs int
	served   int
}

func NewStore(seeds []BucketSeed) func(env.Env) env.Process {
	return func(e env.Env) env.Process { return &Store{e: e, seeds: seeds} }
}

func (s *Store) Start() { s.load() }

func (s *Store) load() {
	b, err := s.e.Disk.Read(stateBlob)
	switch {
	case errors.Is(err, env.ErrNotFound):
		s.st = storeState{}
		for _, sd := range s.seeds {
			if sd.Loc == s.e.ID {
				s.st.put(&bucketState{Name: sd.Name, Status: statusLive, Objs: slices.Clone(sd.Objs)})
			}
			s.st.Ptrs = append(s.st.Ptrs, pointerState{Bucket: sd.Name, Loc: string(sd.Loc)})
		}
		if err := s.persist(); err != nil {
			s.retryLoad()
			return
		}
		s.loaded = true
	case err != nil:
		s.retryLoad()
	default:
		st, derr := decodeState(b)
		if derr != nil {
			s.loadErrs++
			return
		}
		s.st, s.loaded = st, true
	}
}

func (s *Store) retryLoad() {
	s.loadErrs++
	s.e.Clock.AfterFunc(loadRetry, s.load)
}

func (s *Store) persist() error {
	if err := s.e.Disk.Write(stateBlob, s.st.encode()); err != nil {
		return err
	}
	return s.e.Disk.Sync(stateBlob)
}

func (s *Store) OnDatagram(env.NodeID, []byte) {}

func (s *Store) EncodeState() []byte {
	b := []byte{0}
	if s.loaded {
		b[0] = 1
		b = append(b, s.st.encode()...)
	}
	b = binary.AppendUvarint(b, uint64(s.loadErrs))
	return binary.AppendUvarint(b, uint64(s.served))
}

func permanent(format string, args ...any) error {
	return fmt.Errorf("sagasim: "+format+": %w", append(args, saga.ErrPermanent)...)
}

func (s *Store) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	s.served++
	if !s.loaded {
		reply(nil, fmt.Errorf("sagasim: %s still loading: %w", s.e.ID, env.ErrIO))
		return
	}
	op, f, err := decodeFields(req)
	if err != nil {
		reply(nil, err)
		return
	}
	need := map[byte]int{opRegister: 2, opDeregister: 2, opClaim: 2, opRelease: 2, opCopy: 3, opDump: 1, opDiscard: 2,
		opRoot: 1, opFlip: 4, opPointer: 1, opSeal: 2, opPromote: 2, opRetire: 2, opPut: 3}
	if n, ok := need[op]; !ok || len(f) != n {
		reply(nil, fmt.Errorf("sagasim: op %d with %d fields: %w", op, len(f), ErrMalformed))
		return
	}
	if op == opCopy {
		s.copyFrom(string(f[0]), string(f[1]), env.NodeID(f[2]), reply)
		return
	}
	resp, changed, err := s.handle(op, f)
	if err == nil && changed {
		err = s.persist()
	}
	if err != nil {
		reply(nil, err)
		return
	}
	reply(resp, nil)
}

func (s *Store) handle(op byte, f [][]byte) ([]byte, bool, error) {
	name := string(f[0])
	bk := s.st.bucket(name)
	sagaID := ""
	if len(f) > 1 {
		sagaID = string(f[1])
	}
	switch op {
	case opRegister:
		if bk == nil || (bk.Status != statusLive && !(bk.Status == statusSealed && bk.Sealer == sagaID)) {
			return nil, false, permanent("register %s at %s: not a live source", name, s.e.ID)
		}
		if slices.Contains(bk.Regs, sagaID) {
			return nil, false, nil
		}
		bk.Regs = append(bk.Regs, sagaID)
		slices.Sort(bk.Regs)
		return nil, true, nil
	case opDeregister:
		if bk == nil || !slices.Contains(bk.Regs, sagaID) {
			return nil, false, nil
		}
		bk.Regs = slices.DeleteFunc(bk.Regs, func(r string) bool { return r == sagaID })
		return nil, true, nil
	case opClaim:
		switch {
		case bk == nil || bk.Status == statusRetired:
			s.st.put(&bucketState{Name: name, Status: statusStaging, Owner: sagaID})
			return nil, true, nil
		case bk.Status == statusStaging && bk.Owner == sagaID:
			return nil, false, nil
		}
		return nil, false, permanent("claim %s at %s: taken", name, s.e.ID)
	case opRelease:
		if bk == nil || bk.Status != statusStaging || bk.Owner != sagaID {
			return nil, false, nil
		}
		if len(bk.Objs) == 0 {
			s.st.drop(name)
		} else {
			bk.Status, bk.Owner = statusRetired, ""
		}
		return nil, true, nil
	case opDump:
		if bk == nil || (bk.Status != statusLive && bk.Status != statusSealed) {
			return nil, false, permanent("dump %s at %s: no source data", name, s.e.ID)
		}
		return encodeObjs(bk.Objs), false, nil
	case opDiscard:
		if bk != nil && bk.Status == statusStaging && bk.Owner == sagaID && len(bk.Objs) > 0 {
			bk.Objs = nil
			return nil, true, nil
		}
		return nil, false, nil
	case opRoot:
		if bk == nil || bk.Status == statusRetired {
			return nil, false, fmt.Errorf("sagasim: root %s at %s: %w", name, s.e.ID, env.ErrNotFound)
		}
		return Root(bk.Objs), false, nil
	case opFlip:
		if len(f[2]) != 8 {
			return nil, false, fmt.Errorf("sagasim: flip %s: stamp of %d bytes: %w", name, len(f[2]), ErrMalformed)
		}
		p, i := s.st.pointer(name)
		np := pointerState{Bucket: name, Loc: string(f[1]), TS: int64(binary.BigEndian.Uint64(f[2])), Writer: string(f[3])}
		if i >= 0 && (p.TS > np.TS || (p.TS == np.TS && p.Writer >= np.Writer)) {
			return nil, false, nil
		}
		if i < 0 {
			s.st.Ptrs = append(s.st.Ptrs, np)
		} else {
			s.st.Ptrs[i] = np
		}
		return nil, true, nil
	case opPointer:
		p, _ := s.st.pointer(name)
		return encodeFields(0, []byte(p.Loc), binary.BigEndian.AppendUint64(nil, uint64(p.TS)), []byte(p.Writer)), false, nil
	case opSeal:
		switch {
		case bk != nil && bk.Status == statusRetired && bk.Sealer == sagaID:
			return []byte{1}, false, nil
		case bk != nil && bk.Status == statusSealed && bk.Sealer == sagaID:
			return []byte{0}, false, nil
		case bk == nil || bk.Status != statusLive:
			return nil, false, fmt.Errorf("sagasim: seal %s at %s: not live: %w", name, s.e.ID, ErrBusy)
		}
		for _, r := range bk.Regs {
			if r != sagaID {
				return nil, false, fmt.Errorf("sagasim: seal %s at %s: saga %s still registered: %w", name, s.e.ID, r, ErrBusy)
			}
		}
		bk.Status, bk.Sealer = statusSealed, sagaID
		return []byte{0}, true, nil
	case opPromote:
		switch {
		case bk != nil && bk.Status == statusStaging && bk.Owner == sagaID:
			bk.Status = statusLive
			return nil, true, nil
		case bk != nil && bk.Status == statusLive && bk.Owner == sagaID:
			return nil, false, nil
		}
		return nil, false, permanent("promote %s at %s: not staged by %s", name, s.e.ID, sagaID)
	case opRetire:
		switch {
		case bk != nil && bk.Status == statusSealed && bk.Sealer == sagaID:
			bk.Status, bk.Objs, bk.Regs = statusRetired, nil, nil
			return nil, true, nil
		case bk != nil && bk.Status == statusRetired && bk.Sealer == sagaID:
			return nil, false, nil
		}
		return nil, false, permanent("retire %s at %s: not sealed by %s", name, s.e.ID, sagaID)
	case opPut:
		if bk == nil || bk.Status != statusLive {
			return nil, false, fmt.Errorf("sagasim: put %s at %s: %w", name, s.e.ID, ErrMoved)
		}
		kv := KV{Key: string(f[1]), Val: bytes.Clone(f[2])}
		i, found := slices.BinarySearchFunc(bk.Objs, kv.Key, func(o KV, k string) int { return strings.Compare(o.Key, k) })
		if found {
			bk.Objs[i] = kv
		} else {
			bk.Objs = slices.Insert(bk.Objs, i, kv)
		}
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("sagasim: op %d: %w", op, ErrMalformed)
}

func (s *Store) copyFrom(name, sagaID string, from env.NodeID, reply env.Reply) {
	owned := func() *bucketState {
		bk := s.st.bucket(name)
		if bk == nil || bk.Owner != sagaID || bk.Status != statusStaging {
			return nil
		}
		return bk
	}
	if owned() == nil {
		reply(nil, permanent("copy %s into %s: not claimed by %s", name, s.e.ID, sagaID))
		return
	}
	s.e.Net.Request(from, encodeFields(opDump, []byte(name)), dumpTimeout, func(resp []byte, err error) {
		if err != nil {
			reply(nil, fmt.Errorf("sagasim: copy %s from %s: %w", name, from, err))
			return
		}
		objs, err := decodeObjs(resp)
		if err != nil {
			reply(nil, err)
			return
		}
		bk := owned()
		if bk == nil {
			reply(nil, permanent("copy %s into %s: claim lost", name, s.e.ID))
			return
		}
		bk.Objs = objs
		if err := s.persist(); err != nil {
			reply(nil, err)
			return
		}
		reply(Root(objs), nil)
	})
}
