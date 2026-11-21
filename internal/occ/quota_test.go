package occ_test

import (
	"errors"
	"math"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"pgregory.net/rapid"
)

func TestQuotaCheckAndSet(t *testing.T) {
	limit := occ.Usage{Bytes: 100, Objects: 3}
	t.Run("the first update creates the counter with If-None-Match", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		u, p, err := s.ReadQuota("q")
		if err != nil || u != (occ.Usage{}) || p.String() != occ.IfNoneMatch().String() {
			t.Fatalf("read of an absent counter: %v %v", u, err)
		}
		got, err := s.UpdateQuota("q", 3, nil, occ.AddUsage(40, 1, limit))
		if err != nil || got != (occ.Usage{Bytes: 40, Objects: 1}) {
			t.Fatalf("update: %v %v", got, err)
		}
	})
	t.Run("an update over the limit is refused and changes nothing", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		if _, err := s.UpdateQuota("q", 3, nil, occ.AddUsage(90, 1, limit)); err != nil {
			t.Fatal(err)
		}
		before := s.Record("q").ETag()
		if _, err := s.UpdateQuota("q", 3, nil, occ.AddUsage(20, 1, limit)); !errors.Is(err, occ.ErrQuotaExceeded) {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.UpdateQuota("q", 3, nil, occ.AddUsage(0, 3, limit)); !errors.Is(err, occ.ErrQuotaExceeded) {
			t.Fatalf("objects over limit: %v", err)
		}
		if s.Record("q").ETag() != before {
			t.Fatal("a refused update wrote")
		}
		if got, err := s.UpdateQuota("q", 3, nil, occ.AddUsage(-90, -1, limit)); err != nil || got != (occ.Usage{}) {
			t.Fatalf("shrinking: %v %v", got, err)
		}
	})
	t.Run("a stale read is refused and retried", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		calls := 0
		got, err := s.UpdateQuota("q", 3, nil, func(u occ.Usage) (occ.Usage, error) {
			calls++
			if calls == 1 {
				if _, err := s.UpdateQuota("q", 1, nil, occ.AddUsage(5, 1, occ.Usage{})); err != nil {
					t.Fatal(err)
				}
			}
			return occ.AddUsage(10, 1, occ.Usage{})(u)
		})
		if err != nil || calls != 2 || got != (occ.Usage{Bytes: 15, Objects: 2}) {
			t.Fatalf("got %v after %d calls: %v", got, calls, err)
		}
	})
	t.Run("retries are bounded", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		_, err := s.UpdateQuota("q", 2, nil, func(u occ.Usage) (occ.Usage, error) {
			if _, err := s.UpdateQuota("q", 1, nil, occ.AddUsage(1, 0, occ.Usage{})); err != nil {
				t.Fatal(err)
			}
			return u, nil
		})
		if !errors.Is(err, occ.ErrPreconditionFailed) {
			t.Fatalf("err = %v", err)
		}
		if _, err := s.UpdateQuota("q", 0, nil, occ.AddUsage(1, 0, occ.Usage{})); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("zero attempts: %v", err)
		}
	})
	t.Run("partition siblings are resolved by the recount hook, never guessed", func(t *testing.T) {
		a, _ := newTestStore(t, "a", 1, 1)
		b, _ := newTestStore(t, "b", 1, 2)
		if _, err := a.UpdateQuota("q", 1, nil, occ.AddUsage(10, 1, limit)); err != nil {
			t.Fatal(err)
		}
		b.Merge(a.Doc())
		if _, err := a.UpdateQuota("q", 1, nil, occ.AddUsage(60, 1, limit)); err != nil {
			t.Fatal(err)
		}
		if _, err := b.UpdateQuota("q", 1, nil, occ.AddUsage(70, 1, limit)); err != nil {
			t.Fatal(err)
		}
		a.Merge(b.Doc())
		if _, _, err := a.ReadQuota("q"); !errors.Is(err, occ.ErrConflict) {
			t.Fatalf("siblings must surface: %v", err)
		}
		if _, err := a.UpdateQuota("q", 3, nil, occ.AddUsage(1, 0, limit)); !errors.Is(err, occ.ErrConflict) {
			t.Fatalf("without a hook: %v", err)
		}
		recount := func(string) (occ.Usage, error) { return occ.Usage{Bytes: 140, Objects: 3}, nil }
		if _, err := a.UpdateQuota("q", 3, recount, occ.AddUsage(1, 0, limit)); !errors.Is(err, occ.ErrQuotaExceeded) {
			t.Fatalf("the recounted usage is over the limit: %v", err)
		}
		got, err := a.UpdateQuota("q", 3, recount, occ.AddUsage(0, 0, limit))
		if err != nil || got != (occ.Usage{Bytes: 140, Objects: 3}) || a.Record("q").Len() != 1 {
			t.Fatalf("resolution: %v %v", got, err)
		}
		boom := errors.New("boom")
		a.Merge(b.Doc())
		b2, _ := newTestStore(t, "c", 1, 3)
		b2.Merge(b.Doc())
		if _, err := b2.UpdateQuota("q", 1, nil, occ.AddUsage(1, 0, occ.Usage{})); err != nil {
			t.Fatal(err)
		}
		a.Merge(b2.Doc())
		if _, err := a.UpdateQuota("q", 1, func(string) (occ.Usage, error) { return occ.Usage{}, boom }, occ.AddUsage(0, 0, limit)); !errors.Is(err, boom) {
			t.Fatalf("recount error: %v", err)
		}
	})
	t.Run("an unconditional quota write is refused", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		if _, err := s.WriteQuota("q", occ.Usage{}, occ.Unconditional()); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatal(err)
		}
	})
	t.Run("a corrupt counter value is reported", func(t *testing.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		mustPut(t, s, "q", "\xff", occ.IfNoneMatch())
		if _, _, err := s.ReadQuota("q"); !errors.Is(err, occ.ErrMalformed) {
			t.Fatal(err)
		}
		if _, err := s.UpdateQuota("q", 1, nil, occ.AddUsage(1, 0, occ.Usage{})); !errors.Is(err, occ.ErrMalformed) {
			t.Fatal(err)
		}
	})
	t.Run("usage arithmetic never wraps", func(t *testing.T) {
		add := occ.AddUsage(1, 0, occ.Usage{})
		if _, err := add(occ.Usage{Bytes: math.MaxUint64}); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatal(err)
		}
		if _, err := occ.AddUsage(-1, 0, occ.Usage{})(occ.Usage{}); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatal(err)
		}
		if got, err := occ.AddUsage(math.MinInt64, 0, occ.Usage{})(occ.Usage{Bytes: 1 << 63}); err != nil || got.Bytes != 0 {
			t.Fatalf("MinInt64: %v %v", got, err)
		}
	})
	t.Run("usage encoding is canonical", func(t *testing.T) {
		u := occ.Usage{Bytes: 300, Objects: 2}
		back, err := occ.DecodeUsage(u.Encode())
		if err != nil || back != u {
			t.Fatal(err)
		}
		for _, bad := range [][]byte{nil, {0x80, 0x00, 0x00}, {1, 2, 3}} {
			if _, err := occ.DecodeUsage(bad); !errors.Is(err, occ.ErrMalformed) {
				t.Fatalf("%x: %v", bad, err)
			}
		}
	})
}

func TestPropQuotaNoStaleApply(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s, _ := newTestStore(t, "a", 1, 1)
		type read struct {
			u occ.Usage
			p occ.Precondition
		}
		var reads []read
		var model occ.Usage
		writes := 0
		for range rapid.IntRange(1, 50).Draw(t, "steps") {
			switch rapid.IntRange(0, 2).Draw(t, "op") {
			case 0:
				u, p, err := s.ReadQuota("q")
				if err != nil {
					t.Fatal(err)
				}
				if u != model {
					t.Fatalf("read %v, model %v", u, model)
				}
				reads = append(reads, read{u, p})
			case 1:
				if len(reads) == 0 {
					continue
				}
				r := reads[rapid.IntRange(0, len(reads)-1).Draw(t, "which")]
				delta := uint64(rapid.IntRange(1, 10).Draw(t, "delta"))
				fresh := r.u == model && r.p.String() == currentPre(s).String()
				_, err := s.WriteQuota("q", occ.Usage{Bytes: r.u.Bytes + delta, Objects: r.u.Objects}, r.p)
				if fresh != (err == nil) {
					t.Fatalf("fresh=%v but write err=%v", fresh, err)
				}
				if err == nil {
					model.Bytes = r.u.Bytes + delta
					writes++
				} else if !errors.Is(err, occ.ErrPreconditionFailed) {
					t.Fatal(err)
				}
			default:
				delta := int64(rapid.IntRange(1, 10).Draw(t, "add"))
				if _, err := s.UpdateQuota("q", 1, nil, occ.AddUsage(delta, 1, occ.Usage{})); err != nil {
					t.Fatal(err)
				}
				model.Bytes += uint64(delta)
				model.Objects++
				writes++
			}
		}
		u, _, err := s.ReadQuota("q")
		if err != nil || u != model {
			t.Fatalf("final %v, model %v: %v", u, model, err)
		}
	})
}

func currentPre(s *occ.Store) occ.Precondition {
	_, p, _ := s.ReadQuota("q")
	return p
}
