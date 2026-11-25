package objload

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// Op is one kind of request.
type Op int

// The operations. The HTTP metric label each one moves is in brackets.
const (
	OpGet        Op = iota // GET an object of the key space [object]
	OpPut                  // unconditional PUT [object]
	OpHead                 // HEAD [object]
	OpList                 // list a bucket [bucket]
	OpDelete               // unconditional DELETE [object]
	OpTag                  // PUT ?tag [tag]
	OpTags                 // GET ?tags [object]
	OpCreate               // PUT with If-None-Match: * (412 when the key exists) [object]
	OpStalePut             // PUT with an If-Match tag that never matches (412 or 404) [object]
	OpGetMissing           // GET a key that is never written (404) [object]
	OpHealth               // GET /_health [health]
	OpMembers              // GET /_cluster/members [members]
	NumOps
)

var opNames = [NumOps]string{"get", "put", "head", "list", "delete", "tag", "tags", "create", "stale-put", "get-missing", "health", "members"}

func (o Op) String() string {
	if o < 0 || o >= NumOps {
		return fmt.Sprintf("op(%d)", int(o))
	}
	return opNames[o]
}

// Mix holds a relative weight per operation.
type Mix [NumOps]int

// Profiles are the named operation mixes. "simple" is mostly GET and PUT with
// some HEAD, LIST and DELETE; "mixed" adds tags, conditional writes that fail
// with 412, reads of missing keys (404), health and membership requests.
var Profiles = map[string]Mix{
	"simple": {OpGet: 50, OpPut: 30, OpHead: 10, OpList: 5, OpDelete: 5},
	"mixed": {OpGet: 30, OpPut: 20, OpHead: 6, OpList: 6, OpDelete: 5, OpTag: 8, OpTags: 5,
		OpCreate: 6, OpStalePut: 5, OpGetMissing: 6, OpHealth: 2, OpMembers: 1},
}

// Chooser draws operations with probability proportional to their weight.
type Chooser struct {
	cum   [NumOps]int
	total int
}

// NewChooser returns a Chooser for m. It fails when no weight is positive or
// a weight is negative.
func NewChooser(m Mix) (Chooser, error) {
	var c Chooser
	for op, w := range m {
		if w < 0 {
			return Chooser{}, fmt.Errorf("%w: weight %d for %v", ErrConfig, w, Op(op))
		}
		c.total += w
		c.cum[op] = c.total
	}
	if c.total == 0 {
		return Chooser{}, fmt.Errorf("%w: empty operation mix", ErrConfig)
	}
	return c, nil
}

// Pick maps r, uniform in [0, total), to an operation.
func (c Chooser) Pick(r int) Op {
	for op, cum := range c.cum {
		if r < cum {
			return Op(op)
		}
	}
	return NumOps - 1
}

// Total returns the sum of the weights.
func (c Chooser) Total() int { return c.total }

// Request is one planned request.
type Request struct {
	Seq      int64
	Op       Op
	Addr     int
	Bucket   string
	Key      string
	Size     int
	DataSeed uint64
}

// Planner turns a seed into a reproducible sequence of requests.
type Planner struct {
	cfg    Config
	rng    *rand.Rand
	choose Chooser
	seq    int64
}

// NewPlanner returns a Planner for a validated cfg.
func NewPlanner(cfg Config) (*Planner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ch, err := NewChooser(Profiles[cfg.Profile])
	if err != nil {
		return nil, err
	}
	return &Planner{cfg: cfg, rng: rand.New(rand.NewPCG(cfg.Seed, 0x6f626a6c6f6164)), choose: ch}, nil
}

// Next returns the next request of the sequence.
func (p *Planner) Next() Request {
	r := Request{Seq: p.seq, Addr: int(p.seq % int64(len(p.cfg.Addrs)))}
	p.seq++
	r.Op = p.choose.Pick(p.rng.IntN(p.choose.Total()))
	r.Bucket = BucketName(p.cfg.BucketPrefix, p.rng.IntN(p.cfg.Buckets))
	r.Key = fmt.Sprintf("key-%06d", p.rng.IntN(p.cfg.Keys))
	r.Size = p.cfg.MinSize + p.rng.IntN(p.cfg.MaxSize-p.cfg.MinSize+1)
	r.DataSeed = p.rng.Uint64()
	if r.Op == OpGetMissing {
		r.Key = fmt.Sprintf("missing/%016x", r.DataSeed)
	}
	return r
}

// Payload returns n bytes derived from seed.
func Payload(seed uint64, n int) []byte {
	b := make([]byte, n)
	rng := rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)
	for i := 0; i < n; i += 8 {
		v := rng.Uint64()
		for j := i; j < i+8 && j < n; j++ {
			b[j] = byte(v)
			v >>= 8
		}
	}
	return b
}

// StaleETag is the If-Match tag of OpStalePut; no object ever has it.
const StaleETag = "objload-stale-etag"

// Execute sends r to svc.
func Execute(ctx context.Context, svc httpapi.Service, r Request) error {
	var err error
	put := func(cond storage.Condition) {
		_, err = svc.PutObject(ctx, storage.PutRequest{Bucket: r.Bucket, Key: r.Key, Data: Payload(r.DataSeed, r.Size),
			ContentType: "application/octet-stream", Cond: cond})
	}
	switch r.Op {
	case OpGet, OpGetMissing:
		_, _, err = svc.GetObject(ctx, r.Bucket, r.Key)
	case OpPut:
		put(storage.Condition{})
	case OpHead:
		_, err = svc.HeadObject(ctx, r.Bucket, r.Key)
	case OpList:
		_, err = svc.ListObjects(ctx, r.Bucket, "", 100)
	case OpDelete:
		_, err = svc.DeleteObject(ctx, r.Bucket, r.Key, storage.Condition{})
	case OpTag:
		_, err = svc.PutTag(ctx, r.Bucket, r.Key, "load", fmt.Sprintf("v%d", r.Seq), storage.Condition{})
	case OpTags:
		_, err = svc.Tags(ctx, r.Bucket, r.Key)
	case OpCreate:
		put(storage.Condition{IfNoneMatch: true})
	case OpStalePut:
		put(storage.Condition{IfMatch: StaleETag})
	case OpHealth:
		_, err = svc.Health(ctx)
	case OpMembers:
		_, err = svc.Members(ctx)
	default:
		err = fmt.Errorf("%w: unknown operation %v", ErrConfig, r.Op)
	}
	return err
}
