package sagasim

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
)

func unpaddedObjects(n int) []KV {
	out := make([]KV, n)
	for i := range out {
		out[n-1-i] = KV{Key: fmt.Sprintf("o%d", i), Val: []byte("v")}
	}
	return out
}

func seededStore(t *testing.T, objs []KV) *Store {
	t.Helper()
	d := &mapDisk{blobs: map[string][]byte{}}
	s := NewStore([]BucketSeed{{Name: Bucket, Loc: "a", Objs: objs}})(env.Env{ID: "a", Clock: &nopClock{}, Disk: d, Net: &heldNet{}}).(*Store)
	s.Start()
	if !s.loaded {
		t.Fatal("store did not load")
	}
	return s
}

func storedObjs(t *testing.T, s *Store) []KV {
	t.Helper()
	bk := s.st.bucket(Bucket)
	if bk == nil {
		t.Fatal("no bucket")
	}
	if !slices.IsSortedFunc(bk.Objs, func(a, b KV) int { return strings.Compare(a.Key, b.Key) }) {
		t.Fatal("stored object list is not sorted")
	}
	return bk.Objs
}

// TestStoreKeepsObjectsSortedPastOneThousand proves that Store keeps object
// lists sorted for seeds of 2000 and MaxObjects-1 objects in any order, updates
// overwrites in place, collapses duplicate seed keys, and rejects unsorted or
// duplicate keys on decode with ErrMalformed.
func TestStoreKeepsObjectsSortedPastOneThousand(t *testing.T) {
	if got := objects(MaxObjects); !slices.IsSortedFunc(got, func(a, b KV) int { return strings.Compare(a.Key, b.Key) }) {
		t.Fatal("objects(MaxObjects) is not sorted")
	}
	for _, seed := range [][]KV{objects(2000), unpaddedObjects(2000)} {
		s := seededStore(t, seed)
		for _, k := range []string{seed[1000].Key, seed[500].Key, seed[1999].Key} {
			if _, err := ask(s, opPut, Bucket, k, "updated"); err != nil {
				t.Fatalf("overwrite %s: %v", k, err)
			}
		}
		objs := storedObjs(t, s)
		if len(objs) != 2000 {
			t.Fatalf("overwrites changed the object count to %d", len(objs))
		}
		i, found := slices.BinarySearchFunc(objs, seed[1000].Key, func(o KV, k string) int { return strings.Compare(o.Key, k) })
		if !found || string(objs[i].Val) != "updated" {
			t.Fatalf("overwrite of %s not applied", seed[1000].Key)
		}
	}
	s := seededStore(t, unpaddedObjects(MaxObjects-1))
	if _, err := ask(s, opPut, Bucket, "zz", "v"); err != nil {
		t.Fatalf("the put that reaches the bound: %v", err)
	}
	if n := len(storedObjs(t, s)); n != MaxObjects {
		t.Fatalf("object count %d at the bound", n)
	}
	if _, err := ask(s, opPut, Bucket, "o1000", "updated"); err != nil {
		t.Fatalf("an overwrite at the bound: %v", err)
	}
	if _, err := ask(s, opPut, Bucket, "o99999", "v"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("a put past the bound: %v", err)
	}
	if n := len(storedObjs(t, s)); n != MaxObjects {
		t.Fatalf("object count %d after the refused put", n)
	}
	dup := seededStore(t, []KV{{Key: "k", Val: []byte("1")}, {Key: "j", Val: []byte("2")}, {Key: "k", Val: []byte("3")}})
	if objs := storedObjs(t, dup); len(objs) != 2 || string(objs[1].Val) != "3" {
		t.Fatalf("duplicate seed keys: %+v", objs)
	}
	for _, bad := range [][]KV{{{Key: "b"}, {Key: "a"}}, {{Key: "a"}, {Key: "a"}}} {
		b, err := encodeObjs(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeObjs(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("decoding %+v: %v", bad, err)
		}
	}
}

// TestRunClosuresShareNoReport proves that one NemesisRun or DuelRun closure
// created with a nil report can be invoked concurrently without racing.
func TestRunClosuresShareNoReport(t *testing.T) {
	np := NemesisParams{Start: env.Time(300 * time.Millisecond), Phases: 1, Chaos: time.Second, Heal: time.Second, MaxGap: 300 * time.Millisecond,
		Converge: time.Minute, Writes: 2, WriteGap: 300 * time.Millisecond, Tick: 100 * time.Millisecond}
	runs := []func(int64) *sim.Sim{NemesisRun(np, nil), DuelRun(DuelParams{Converge: time.Minute, MinPartition: time.Second}, nil)}
	for _, fn := range runs {
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for g := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[g] = fn(int64(g + 1)).Err()
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	}
}
