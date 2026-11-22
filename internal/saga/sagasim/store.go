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

// Sentinel errors returned by model stores, Ops, and cluster checks:
//   - ErrBusy: source cannot be sealed while another saga is registered or not live,
//     or a relayed copy was overtaken by a later bucket operation.
//   - ErrMoved: a client put targeted a store that does not hold the bucket live.
//   - ErrUnsettled: routing pointer replicas returned different locations.
//   - ErrCheck: a post-run or invariant verification check failed.
//   - ErrMalformed: an RPC message, object list, or persisted store blob failed decoding.
var (
	ErrBusy      = errors.New("sagasim: bucket busy")
	ErrMoved     = errors.New("sagasim: bucket not writable here")
	ErrUnsettled = errors.New("sagasim: pointer replicas disagree")
	ErrCheck     = errors.New("sagasim: check failed")
	ErrMalformed = errors.New("sagasim: malformed message")
)

// Wire status codes for sagasim sentinels within the saga 144..159 block.
const (
	CodeBusy      env.Code = env.CodeAppMin + 88
	CodeMoved     env.Code = env.CodeAppMin + 89
	CodeUnsettled env.Code = env.CodeAppMin + 90
	CodeMalformed env.Code = env.CodeAppMin + 91
)

// Codes returns saga.Codes extended with this package's wire error mappings.
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

// KV is one key/value object stored in a model bucket.
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

// Root returns the SHA-256 digest over objs in slice order, used as the model's
// Merkle root for copy and catch-up verification.
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

// newer defines the total LWW order for routing pointers: timestamp, then
// writer ID, then target location so equal stamps still converge deterministically.
func newer(a, b pointerState) bool {
	if a.TS != b.TS {
		return a.TS > b.TS
	}
	if a.Writer != b.Writer {
		return a.Writer > b.Writer
	}
	return a.Loc > b.Loc
}

// BucketSeed specifies the initial location and objects of a bucket loaded by
// NewStore when a store starts with an empty disk.
type BucketSeed struct {
	Name string
	Loc  env.NodeID
	Objs []KV
}

// Store is a simulated storage node hosting bucket state and a routing-pointer
// replica, persisted as one synced blob on env.Disk. Operations are idempotent
// per saga ID, and two-hop copies are fenced by a per-bucket generation counter.
type Store struct {
	e        env.Env
	seeds    []BucketSeed
	st       storeState
	gens     map[string]uint64
	loaded   bool
	loadErrs int
	served   int
	fenced   int
}

// NewStore returns a process factory that creates a Store initialized with seeds.
func NewStore(seeds []BucketSeed) func(env.Env) env.Process {
	return func(e env.Env) env.Process { return &Store{e: e, seeds: seeds, gens: map[string]uint64{}} }
}

// fence advances bucket's generation so any in-flight copyFrom reply is rejected.
func (s *Store) fence(bucket string) uint64 {
	s.gens[bucket]++
	return s.gens[bucket]
}

// Start loads persisted store state from disk, or initializes it from seeds.
func (s *Store) Start() { s.load() }

func (s *Store) load() {
	b, err := s.e.Disk.Read(stateBlob)
	switch {
	case errors.Is(err, env.ErrNotFound):
		s.st = storeState{}
		for _, sd := range s.seeds {
			if sd.Loc == s.e.ID {
				s.st.put(&bucketState{Name: sd.Name, Status: statusLive, Objs: sortedObjs(sd.Objs)})
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

// sortedObjs returns a key-sorted copy of objs with duplicate keys collapsed
// (last entry wins) so opPut's binary search holds for any seed order.
func sortedObjs(objs []KV) []KV {
	out := slices.Clone(objs)
	slices.SortStableFunc(out, func(a, b KV) int { return strings.Compare(a.Key, b.Key) })
	keep := out[:0]
	for _, kv := range out {
		if n := len(keep); n > 0 && keep[n-1].Key == kv.Key {
			keep[n-1] = kv
			continue
		}
		keep = append(keep, kv)
	}
	return keep
}

func (s *Store) retryLoad() {
	s.loadErrs++
	s.e.Clock.AfterFunc(loadRetry, s.load)
}

func (s *Store) persist() error {
	b, err := s.st.encode()
	if err != nil {
		return err
	}
	if err := s.e.Disk.Write(stateBlob, b); err != nil {
		return err
	}
	return s.e.Disk.Sync(stateBlob)
}

// OnDatagram ignores datagrams; Store communicates only via requests.
func (s *Store) OnDatagram(env.NodeID, []byte) {}

// EncodeState returns a deterministic digest input for s's volatile and
// persisted state.
func (s *Store) EncodeState() []byte {
	b := []byte{0}
	if s.loaded {
		b[0] = 1
		enc, err := s.st.encode()
		if err != nil {
			b[0] = 2
		}
		b = append(b, enc...)
	}
	b = binary.AppendUvarint(b, uint64(s.loadErrs))
	b = binary.AppendUvarint(b, uint64(s.fenced))
	return binary.AppendUvarint(b, uint64(s.served))
}

func permanent(format string, args ...any) error {
	return fmt.Errorf("sagasim: "+format+": %w", append(args, saga.ErrPermanent)...)
}

// OnRequest decodes and executes one store RPC, persisting state before replying
// on any mutation. While loading, all requests fail retryably with env.ErrIO.
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
	if err := checkRequest(op, f); err != nil {
		reply(nil, err)
		return
	}
	if op == opCopy {
		s.copyFrom(string(f[0]), string(f[1]), env.NodeID(f[2]), reply)
		return
	}
	switch op {
	case opClaim, opRelease, opDiscard, opPromote:
		s.fence(string(f[0]))
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

func checkRequest(op byte, f [][]byte) error {
	for i, x := range f {
		switch {
		case op == opPut && i == 1:
			if len(x) > MaxKey {
				return tooLarge("object key", len(x), MaxKey)
			}
		case op == opPut && i == 2:
			if len(x) > MaxValue {
				return tooLarge("object value", len(x), MaxValue)
			}
		case len(x) > MaxName:
			return tooLarge("request field", len(x), MaxName)
		}
	}
	return nil
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
		if len(bk.Regs) >= MaxRegs {
			return nil, false, fmt.Errorf("register %s at %s: %w", name, s.e.ID, tooLarge("registration list", len(bk.Regs)+1, MaxRegs))
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
		case bk == nil && len(s.st.Buckets) >= MaxBuckets:
			return nil, false, fmt.Errorf("claim %s at %s: %w", name, s.e.ID, tooLarge("bucket list", len(s.st.Buckets)+1, MaxBuckets))
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
			// Leave un-discarded objects visible as retired orphans so copy
			// compensation is required and cannot be masked by Unclaim.
			bk.Status, bk.Owner = statusRetired, ""
		}
		return nil, true, nil
	case opDump:
		if bk == nil || (bk.Status != statusLive && bk.Status != statusSealed) {
			return nil, false, permanent("dump %s at %s: no source data", name, s.e.ID)
		}
		b, err := encodeObjs(bk.Objs)
		return b, false, err
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
		if i >= 0 && !newer(np, p) {
			return nil, false, nil
		}
		if i < 0 && len(s.st.Ptrs) >= MaxBuckets {
			return nil, false, fmt.Errorf("flip %s at %s: %w", name, s.e.ID, tooLarge("pointer list", len(s.st.Ptrs)+1, MaxBuckets))
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
		} else if len(bk.Objs) >= MaxObjects {
			return nil, false, fmt.Errorf("put %s at %s: %w", name, s.e.ID, tooLarge("object list", len(bk.Objs)+1, MaxObjects))
		} else {
			bk.Objs = slices.Insert(bk.Objs, i, kv)
		}
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("sagasim: op %d: %w", op, ErrMalformed)
}

// copyFrom pulls the bucket's objects from source node from and stages them
// locally, fencing the two-hop reply against concurrent discard, release,
// claim, promote, or newer copy operations.
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
	gen := s.fence(name)
	s.e.Net.Request(from, encodeFields(opDump, []byte(name)), dumpTimeout, func(resp []byte, err error) {
		if s.gens[name] != gen {
			s.fenced++
			reply(nil, fmt.Errorf("sagasim: copy %s into %s: overtaken by a later operation: %w", name, s.e.ID, ErrBusy))
			return
		}
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
