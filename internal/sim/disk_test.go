package sim_test

import (
	"bytes"
	"errors"
	"math/bits"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
)

func oneNode(t *testing.T, seed int64) *harness {
	return newHarness(t, sim.Config{Seed: seed}, spec("a", "r"))
}

// TestDiskOperations proves Write, Append, Read copying, sorted prefix List,
// Delete, and error returns for missing blobs and empty names.
func TestDiskOperations(t *testing.T) {
	h := oneNode(t, 1)
	d := h.rec("a").e.Disk
	mustNil(t, d.Write("b/2", []byte("two")))
	mustNil(t, d.Write("b/1", []byte("one")))
	mustNil(t, d.Write("c", []byte("x")))
	mustNil(t, d.Append("b/1", []byte("+")))
	mustNil(t, d.Append("new", []byte("n")))

	got, err := d.Read("b/1")
	if err != nil || string(got) != "one+" {
		t.Fatalf("Read = %q %v", got, err)
	}
	got[0] = 'X'
	if again, _ := d.Read("b/1"); string(again) != "one+" {
		t.Fatalf("Read returned shared memory: %q", again)
	}
	names, err := d.List("b/")
	if err != nil || len(names) != 2 || names[0] != "b/1" || names[1] != "b/2" {
		t.Fatalf("List = %v %v", names, err)
	}
	all, _ := d.List("")
	if len(all) != 4 {
		t.Fatalf("List all = %v", all)
	}
	mustNil(t, d.Delete("c"))
	if _, err := d.Read("c"); !errors.Is(err, env.ErrNotFound) {
		t.Fatalf("Read deleted = %v", err)
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"delete of a missing blob", d.Delete("c"), env.ErrNotFound},
		{"sync of a missing blob", d.Sync("never"), env.ErrNotFound},
		{"read of an empty name", func() error { _, err := d.Read(""); return err }(), env.ErrInvalidArgument},
		{"write of an empty name", d.Write("", nil), env.ErrInvalidArgument},
		{"append to an empty name", d.Append("", nil), env.ErrInvalidArgument},
		{"sync of an empty name", d.Sync(""), env.ErrInvalidArgument},
		{"delete of an empty name", d.Delete(""), env.ErrInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, tc.want) {
				t.Fatalf("err = %v, want %v", tc.err, tc.want)
			}
		})
	}
}

// TestDiskWriteCopiesInput proves that mutating the slice passed to Write does
// not alter stored blob contents.
func TestDiskWriteCopiesInput(t *testing.T) {
	h := oneNode(t, 1)
	d := h.rec("a").e.Disk
	buf := []byte("abc")
	mustNil(t, d.Write("x", buf))
	buf[0] = 'Z'
	if got, _ := d.Read("x"); string(got) != "abc" {
		t.Fatalf("disk kept caller memory: %q", got)
	}
}

// TestDiskFaults proves silent single-bit rot, durable damage-mark tracking
// across unsynced edits and crashes, wipes, crash rollback, read-error
// injection, closed-handle errors, and unknown-node errors.
func TestDiskFaults(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, h *harness)
	}{
		{"bit-rot is silent and flips exactly one bit", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			orig := bytes.Repeat([]byte{0x55}, 64)
			mustNil(t, d.Write("blob", orig))
			mustNil(t, d.Sync("blob"))
			mustNil(t, h.s.BitRot("a", "blob"))
			got, err := d.Read("blob")
			if err != nil {
				t.Fatalf("Read after rot returned error %v", err)
			}
			flipped := 0
			for i := range got {
				flipped += bits.OnesCount8(got[i] ^ orig[i])
			}
			if flipped != 1 {
				t.Fatalf("%d bits flipped", flipped)
			}
			dur, _ := h.s.Disk("a").PeekDurable("blob")
			if !bytes.Equal(dur, got) {
				t.Fatal("durable copy not rotted")
			}
			if dm := h.s.Disk("a").Damaged(); len(dm) != 1 || dm[0] != "blob" {
				t.Fatalf("Damaged = %v", dm)
			}
			mustNil(t, d.Write("blob", orig))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 1 {
				t.Fatalf("an unsynced rewrite cleared the damage mark: %v", dm)
			}
			mustNil(t, d.Sync("blob"))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 0 {
				t.Fatalf("a synced rewrite did not clear damage: %v", dm)
			}
		}},
		{"a crash before the repair is synced leaves the blob damaged and marked", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			orig := bytes.Repeat([]byte{0x55}, 64)
			mustNil(t, d.Write("blob", orig))
			mustNil(t, d.Sync("blob"))
			mustNil(t, h.s.BitRot("a", "blob"))
			mustNil(t, d.Write("blob", orig))
			mustNil(t, h.s.Crash("a"))
			mustNil(t, h.s.Restart("a"))
			h.run(0)
			got, err := h.rec("a").e.Disk.Read("blob")
			if err != nil || bytes.Equal(got, orig) {
				t.Fatalf("blob after crash = %x %v, want the rotted durable copy", got, err)
			}
			if dm := h.s.Disk("a").Damaged(); len(dm) != 1 || dm[0] != "blob" {
				t.Fatalf("Damaged = %v", dm)
			}
		}},
		{"an unsynced delete of a rotted blob resurrects it marked after a crash", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("blob", []byte("payload")))
			mustNil(t, d.Sync("blob"))
			mustNil(t, h.s.BitRot("a", "blob"))
			mustNil(t, d.Delete("blob"))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 1 {
				t.Fatalf("an unsynced delete cleared the mark: %v", dm)
			}
			mustNil(t, h.s.Crash("a"))
			if _, ok := h.s.Disk("a").Peek("blob"); !ok || len(h.s.Disk("a").Damaged()) != 1 {
				t.Fatalf("rotted blob not resurrected with its mark: %v", h.s.Disk("a").Damaged())
			}
		}},
		{"a synced delete clears the mark", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("blob", []byte("payload")))
			mustNil(t, d.Sync("blob"))
			mustNil(t, h.s.BitRot("a", "blob"))
			mustNil(t, d.Delete("blob"))
			mustNil(t, d.Sync("blob"))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 0 {
				t.Fatalf("Damaged = %v", dm)
			}
		}},
		{"a wiped blob rewritten but not synced stays marked across a crash", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("blob", []byte("payload")))
			mustNil(t, d.Sync("blob"))
			mustNil(t, h.s.WipeDisk("a"))
			mustNil(t, d.Write("blob", []byte("payload")))
			mustNil(t, h.s.Crash("a"))
			if _, ok := h.s.Disk("a").Peek("blob"); ok || len(h.s.Disk("a").Damaged()) != 1 {
				t.Fatalf("wiped blob after crash: Damaged = %v", h.s.Disk("a").Damaged())
			}
		}},
		{"rot under a dirty blob is repaired by syncing it", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("w", []byte("payload")))
			mustNil(t, d.Sync("w"))
			mustNil(t, d.Write("l", []byte("payload")))
			mustNil(t, d.Sync("l"))
			mustNil(t, d.Write("w", []byte("newer")))
			mustNil(t, d.Append("l", []byte("+tail")))
			mustNil(t, h.s.BitRot("a", "w"))
			mustNil(t, h.s.BitRot("a", "l"))
			mustNil(t, d.Sync("w"))
			mustNil(t, d.Sync("l"))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 0 {
				t.Fatalf("Damaged = %v", dm)
			}
			if got, _ := h.s.Disk("a").PeekDurable("l"); string(got) != "payload+tail" {
				t.Fatalf("l = %q", got)
			}
		}},
		{"appending onto a rotted blob and syncing keeps the mark", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("l", []byte("payload")))
			mustNil(t, d.Sync("l"))
			mustNil(t, h.s.BitRot("a", "l"))
			mustNil(t, d.Append("l", []byte("+tail")))
			mustNil(t, d.Sync("l"))
			if dm := h.s.Disk("a").Damaged(); len(dm) != 1 {
				t.Fatalf("Damaged = %v", dm)
			}
		}},
		{"bit-rot needs a durable blob with bytes", func(t *testing.T, h *harness) {
			if err := h.s.BitRot("a", "none"); !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("missing: %v", err)
			}
			mustNil(t, h.rec("a").e.Disk.Write("empty", nil))
			mustNil(t, h.rec("a").e.Disk.Sync("empty"))
			if err := h.s.BitRot("a", "empty"); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("empty: %v", err)
			}
		}},
		{"wipe loses the whole disk", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("x", []byte("1")))
			mustNil(t, d.Sync("x"))
			mustNil(t, d.Write("y", []byte("2")))
			mustNil(t, h.s.WipeDisk("a"))
			names, _ := d.List("")
			if len(names) != 0 || len(h.s.Disk("a").Names()) != 0 {
				t.Fatalf("names after wipe: %v", names)
			}
			if _, ok := h.s.Disk("a").PeekDurable("x"); ok {
				t.Fatal("durable layer survived wipe")
			}
			if len(h.s.Disk("a").Damaged()) != 2 {
				t.Fatalf("Damaged = %v", h.s.Disk("a").Damaged())
			}
		}},
		{"crash drops unsynced writes, appends and deletes", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("kept", []byte("v1")))
			mustNil(t, d.Sync("kept"))
			mustNil(t, d.Write("kept", []byte("v2")))
			mustNil(t, d.Write("lost", []byte("x")))
			mustNil(t, d.Write("del", []byte("d")))
			mustNil(t, d.Sync("del"))
			mustNil(t, d.Delete("del"))
			mustNil(t, d.Write("gone", []byte("g")))
			mustNil(t, d.Sync("gone"))
			mustNil(t, d.Delete("gone"))
			mustNil(t, d.Sync("gone"))
			mustNil(t, h.s.Crash("a"))
			mustNil(t, h.s.Restart("a"))
			h.run(0)
			d2 := h.rec("a").e.Disk
			if got, _ := d2.Read("kept"); string(got) != "v1" {
				t.Fatalf("kept = %q", got)
			}
			if _, err := d2.Read("lost"); !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("lost: %v", err)
			}
			if got, _ := d2.Read("del"); string(got) != "d" {
				t.Fatalf("unsynced delete survived crash: %q", got)
			}
			if _, err := d2.Read("gone"); !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("synced delete undone: %v", err)
			}
		}},
		{"a plain crash drops the whole unsynced append", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("log", []byte("AAAA")))
			mustNil(t, d.Sync("log"))
			mustNil(t, d.Append("log", []byte("BBBB")))
			mustNil(t, h.s.Crash("a"))
			if got, _ := h.s.Disk("a").Peek("log"); string(got) != "AAAA" {
				t.Fatalf("log = %q", got)
			}
		}},
		{"a read error fails the next reads with ErrIO then clears", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("x", []byte("1")))
			mustNil(t, h.s.FailReads("a", "x", 2))
			for i := 0; i < 2; i++ {
				if _, err := d.Read("x"); !errors.Is(err, env.ErrIO) {
					t.Fatalf("read %d: %v", i, err)
				}
			}
			if got, err := d.Read("x"); err != nil || string(got) != "1" {
				t.Fatalf("third read %q %v", got, err)
			}
		}},
		{"a read error on every blob", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("x", []byte("1")))
			mustNil(t, d.Write("y", []byte("1")))
			mustNil(t, h.s.FailReads("a", "", 2))
			_, e1 := d.Read("x")
			_, e2 := d.Read("y")
			_, e3 := d.Read("y")
			if !errors.Is(e1, env.ErrIO) || !errors.Is(e2, env.ErrIO) || e3 != nil {
				t.Fatalf("errors %v %v %v", e1, e2, e3)
			}
		}},
		{"a crashed incarnation's disk is closed", func(t *testing.T, h *harness) {
			d := h.rec("a").e.Disk
			mustNil(t, h.s.Crash("a"))
			_, rerr := d.Read("x")
			_, lerr := d.List("")
			for _, err := range []error{rerr, lerr, d.Write("x", nil), d.Append("x", nil), d.Sync("x"), d.Delete("x")} {
				if !errors.Is(err, env.ErrClosed) {
					t.Fatalf("err = %v, want ErrClosed", err)
				}
			}
		}},
		{"disk faults name unknown nodes", func(t *testing.T, h *harness) {
			for _, err := range []error{h.s.BitRot("zz", "x"), h.s.WipeDisk("zz"), h.s.FailReads("zz", "x", 1)} {
				if !errors.Is(err, sim.ErrUnknownNode) {
					t.Fatalf("err = %v", err)
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, oneNode(t, 1))
		})
	}
}

// TestTornTailKeepsAPrefixOfTheUnsyncedAppend proves that CrashTorn preserves
// durable bytes plus a seeded prefix of an unsynced Append, including strict
// partial tails.
func TestTornTailKeepsAPrefixOfTheUnsyncedAppend(t *testing.T) {
	partial := 0
	for seed := int64(1); seed <= 40; seed++ {
		h := oneNode(t, seed)
		d := h.rec("a").e.Disk
		mustNil(t, d.Write("log", []byte("AAAA")))
		mustNil(t, d.Sync("log"))
		mustNil(t, d.Append("log", []byte("BBBBBBBB")))
		mustNil(t, h.s.CrashTorn("a"))
		got, ok := h.s.Disk("a").Peek("log")
		dur, _ := h.s.Disk("a").PeekDurable("log")
		if !ok || !bytes.HasPrefix([]byte("AAAABBBBBBBB"), got) || len(got) < 4 || !bytes.Equal(got, dur) {
			t.Fatalf("seed %d: log after torn crash = %q", seed, got)
		}
		if len(got) > 4 && len(got) < 12 {
			partial++
		}
	}
	if partial == 0 {
		t.Fatal("no seed produced a torn tail")
	}
}

// TestTornCrashOnlyTearsAppendOnlyBlobs proves that CrashTorn reverts an
// unsynced full Write completely rather than leaving a partial overwrite.
func TestTornCrashOnlyTearsAppendOnlyBlobs(t *testing.T) {
	h := oneNode(t, 3)
	d := h.rec("a").e.Disk
	mustNil(t, d.Write("w", []byte("old")))
	mustNil(t, d.Sync("w"))
	mustNil(t, d.Write("w", []byte("newer-and-longer")))
	mustNil(t, h.s.CrashTorn("a"))
	if got, _ := h.s.Disk("a").Peek("w"); string(got) != "old" {
		t.Fatalf("overwrite torn: %q", got)
	}
}

// TestTornCrashCanTearTheFirstAppendOfANewBlob proves that CrashTorn on a new
// blob created via Append can leave either an absent blob or a durable non-empty
// prefix.
func TestTornCrashCanTearTheFirstAppendOfANewBlob(t *testing.T) {
	const rec = "FIRST_RECORD"
	absent, partial := 0, 0
	for seed := int64(1); seed <= 60; seed++ {
		h := oneNode(t, seed)
		mustNil(t, h.rec("a").e.Disk.Append("saga/1", []byte(rec)))
		mustNil(t, h.s.CrashTorn("a"))
		got, ok := h.s.Disk("a").Peek("saga/1")
		dur, dok := h.s.Disk("a").PeekDurable("saga/1")
		if ok != dok || !bytes.Equal(got, dur) {
			t.Fatalf("seed %d: written %q %v, durable %q %v", seed, got, ok, dur, dok)
		}
		switch {
		case !ok:
			absent++
		case len(got) == 0 || !bytes.HasPrefix([]byte(rec), got):
			t.Fatalf("seed %d: torn first record = %q", seed, got)
		case len(got) < len(rec):
			partial++
		}
	}
	if absent == 0 || partial == 0 {
		t.Fatalf("absent %d, partial %d: want both outcomes", absent, partial)
	}
}
