package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
)

// Node identifies one cluster member and its placement region across both tiers.
type Node struct {
	ID     string
	Region string
}

// Call holds the inputs, virtual- or wall-clock timing, and terminal result of
// one asynchronous client API operation issued through a Cluster.
type Call struct {
	Op     string
	At     Node
	Bucket string
	Key    string
	Sum    [32]byte
	Size   int
	Info   storage.ObjectInfo
	Infos  []storage.ObjectInfo
	Data   []byte
	Tag    storage.TagInfo
	Tags   []storage.TagInfo
	ACL    storage.ACLInfo
	Quota  storage.QuotaInfo
	Mig    storage.MigrationInfo
	Err    error
	Began  time.Duration
	Ended  time.Duration
	done   bool
	wait   chan struct{}
}

// Done reports whether the call has reached a terminal result.
func (c *Call) Done() bool { return c.done }

// ShardFile pairs a shard's relative blob name with its raw bytes on disk.
type ShardFile struct {
	Name string
	Blob []byte
}

// Cluster is the common driver interface implemented by the simulated cluster
// (tier S) and the real-process cluster (tier R) so scenarios share one
// definition. Object API methods return a *Call that completes when Wait
// returns; random choices must come from Rand so tier S stays deterministic.
type Cluster interface {
	Tier() string
	Nodes() []Node
	Regions() []string
	Layout() storage.Layout
	Rand() *rand.Rand
	Now() time.Duration

	CreateBucket(at Node, spec storage.BucketSpec) *Call
	Put(at Node, bucket, key string, data []byte, cond storage.Condition) *Call
	Get(at Node, bucket, key string) *Call
	Delete(at Node, bucket, key string, cond storage.Condition) *Call
	List(at Node, bucket string) *Call
	PutTag(at Node, bucket, key, name, value string, cond storage.Condition) *Call
	PutACL(at Node, bucket, principal, perm string, cond storage.Condition) *Call
	Tags(at Node, bucket, key string) *Call
	ACL(at Node, bucket, principal string) *Call
	SetQuota(at Node, bucket string, bytes, objects uint64) *Call
	Quota(at Node, bucket string) *Call
	StartMigration(at Node, bucket, to string) *Call
	Migration(at Node, id string) *Call

	Wait(calls ...*Call) error
	Sleep(d time.Duration) error
	Await(what string, bound time.Duration, cond func() (bool, error)) (time.Duration, error)

	PartitionRegion(region string) error
	Partition(side, other []Node) error
	Heal() error
	Crash(n Node) error
	Restart(n Node) error
	Wipe(n Node) error
	Up(n Node) bool

	Shards(n Node) ([]ShardFile, error)
	Rot(n Node, name string) error

	Health(n Node) (storage.Health, error)
	Members(n Node) ([]storage.MemberInfo, error)
	Converged() (bool, error)
	Ledger() *Ledger
}

var errScenario = errors.New("e2e: scenario check failed")

func failf(format string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, a...), errScenario)
}

func inRegion(c Cluster, r string) []Node {
	var out []Node
	for _, n := range c.Nodes() {
		if n.Region == r {
			out = append(out, n)
		}
	}
	return out
}

func outside(c Cluster, r string) []Node {
	var out []Node
	for _, n := range c.Nodes() {
		if n.Region != r {
			out = append(out, n)
		}
	}
	return out
}

func upNodes(c Cluster) []Node {
	var out []Node
	for _, n := range c.Nodes() {
		if c.Up(n) {
			out = append(out, n)
		}
	}
	return out
}

func pick(c Cluster, from []Node) Node {
	var up []Node
	for _, n := range from {
		if c.Up(n) {
			up = append(up, n)
		}
	}
	if len(up) == 0 {
		return from[c.Rand().IntN(len(from))]
	}
	return up[c.Rand().IntN(len(up))]
}

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := 0; i < n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8 && i+j < n; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

type attempt struct {
	bucket, key string
	sum         [32]byte
	size        int
}

// Ledger records every client PUT attempt's SHA-256 and checks every successful
// GET against what clients wrote, independently of cluster metadata.
type Ledger struct {
	mu       sync.Mutex
	attempts map[string][]attempt
	byETag   map[string]attempt
	acked    []*Call
	meta     []*Call
	reads    int
	wrong    []string
}

func newLedger() *Ledger {
	return &Ledger{attempts: map[string][]attempt{}, byETag: map[string]attempt{}}
}

func objID(bucket, key string) string { return bucket + "/" + key }

// attempt records a PUT's digest before transmission so a write whose reply is
// lost in transit is still recognized if a later GET reads it back.
func (l *Ledger) attempt(k *Call, data []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k.Sum = sha256.Sum256(data)
	k.Size = len(data)
	id := objID(k.Bucket, k.Key)
	l.attempts[id] = append(l.attempts[id], attempt{bucket: k.Bucket, key: k.Key, sum: k.Sum, size: k.Size})
}

// settle records a completed call and verifies every successful GET immediately.
func (l *Ledger) settle(k *Call) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch k.Op {
	case "put":
		if k.Err == nil {
			l.byETag[k.Info.ETag] = attempt{bucket: k.Bucket, key: k.Key, sum: k.Sum, size: k.Size}
			l.acked = append(l.acked, k)
		}
	case "get":
		if k.Err == nil {
			l.reads++
			if err := l.checkRead(k); err != nil {
				l.wrong = append(l.wrong, err.Error())
			}
		}
	case "delete", "tag", "acl", "quota-set":
		if k.Err == nil {
			l.meta = append(l.meta, k)
		}
	}
}

// checkRead requires a GET's bytes to match its reported size, content hash,
// exact acknowledged ETag payload if known, or at least one PUT attempt on that
// key when the winning PUT's reply was lost.
func (l *Ledger) checkRead(k *Call) error {
	sum := sha256.Sum256(k.Data)
	if k.Info.Size != int64(len(k.Data)) {
		return fmt.Errorf("get %s/%s from %s: %d bytes, size %d", k.Bucket, k.Key, k.At.ID, len(k.Data), k.Info.Size)
	}
	if k.Info.ContentSHA != "" && k.Info.ContentSHA != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("get %s/%s from %s: bytes do not hash to the version's content hash", k.Bucket, k.Key, k.At.ID)
	}
	if a, ok := l.byETag[k.Info.ETag]; ok {
		if a.bucket != k.Bucket || a.key != k.Key || a.sum != sum {
			return fmt.Errorf("get %s/%s from %s: version %s returned bytes other than the ones written under it", k.Bucket, k.Key, k.At.ID, short(k.Info.ETag))
		}
		return nil
	}
	if !slices.ContainsFunc(l.attempts[objID(k.Bucket, k.Key)], func(a attempt) bool { return a.sum == sum }) {
		return fmt.Errorf("get %s/%s from %s: %d bytes that no client ever wrote to this key", k.Bucket, k.Key, k.At.ID, len(k.Data))
	}
	return nil
}

// Wrong returns nil if every GET matched a valid client write, or an error
// reporting the count and first mismatch.
func (l *Ledger) Wrong() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.wrong) == 0 {
		return nil
	}
	return failf("%d reads returned wrong bytes; first: %s", len(l.wrong), l.wrong[0])
}

// Acked returns a copy of all acknowledged PUT calls recorded so far.
func (l *Ledger) Acked() []*Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.acked)
}

// Meta returns a copy of all acknowledged metadata-mutating calls (delete, tag,
// ACL, quota-set) recorded so far.
func (l *Ledger) Meta() []*Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.meta)
}

// Reads returns the number of successful GET calls checked by the ledger.
func (l *Ledger) Reads() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reads
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func counter(h storage.Health, name string) uint64 {
	for _, c := range h.Counters {
		if c.Name == name {
			return c.Value
		}
	}
	return 0
}
