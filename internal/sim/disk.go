package sim

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// blob stores one named entry's written (data/exists) and durable (dur/dExists)
// layers. rot tracks whether the written layer currently carries a bit-rot flip
// so Sync knows whether committing it repairs durable damage.
type blob struct {
	data       []byte
	exists     bool
	dur        []byte
	dExists    bool
	dirty      bool
	appendOnly bool
	rot        bool
}

// Disk is a node's simulated in-memory blob store with separate written and
// durable layers, bit-rot and wipe damage tracking, and injected read errors.
type Disk struct {
	s         *Sim
	blobs     map[string]*blob
	damaged   map[string]bool
	failReads map[string]int
	failAny   int
}

// sortedKeys returns map keys in lexicographical order so map traversal stays
// deterministic across runs and processes.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (h *handle) diskOp(op, name string) (*Disk, error) {
	if h.closed() {
		return nil, fmt.Errorf("sim: disk %s %s on %s: %w", op, name, h.n.id, env.ErrClosed)
	}
	if name == "" && op != "list" {
		return nil, fmt.Errorf("sim: disk %s on %s: empty name: %w", op, h.n.id, env.ErrInvalidArgument)
	}
	return h.n.disk, nil
}

func (h *handle) Read(name string) ([]byte, error) {
	d, err := h.diskOp("read", name)
	if err != nil {
		return nil, err
	}
	if d.failAny > 0 {
		d.failAny--
		return nil, fmt.Errorf("sim: disk read %s on %s: %w", name, h.n.id, env.ErrIO)
	}
	if c := d.failReads[name]; c > 0 {
		if c == 1 {
			delete(d.failReads, name)
		} else {
			d.failReads[name] = c - 1
		}
		return nil, fmt.Errorf("sim: disk read %s on %s: %w", name, h.n.id, env.ErrIO)
	}
	b := d.blobs[name]
	if b == nil || !b.exists {
		return nil, fmt.Errorf("sim: disk read %s on %s: %w", name, h.n.id, env.ErrNotFound)
	}
	return clone(b.data), nil
}

func (d *Disk) get(name string) *blob {
	b := d.blobs[name]
	if b == nil {
		b = &blob{}
		d.blobs[name] = b
	}
	return b
}

func (h *handle) Write(name string, data []byte) error {
	d, err := h.diskOp("write", name)
	if err != nil {
		return err
	}
	b := d.get(name)
	b.data = append(make([]byte, 0, len(data)), data...)
	b.exists, b.dirty, b.appendOnly, b.rot = true, true, false, false
	return nil
}

func (h *handle) Append(name string, data []byte) error {
	d, err := h.diskOp("append", name)
	if err != nil {
		return err
	}
	b := d.get(name)
	if !b.dirty {
		b.appendOnly = true
	}
	b.data = append(b.data, data...)
	b.exists, b.dirty = true, true
	return nil
}

func (h *handle) Sync(name string) error {
	d, err := h.diskOp("sync", name)
	if err != nil {
		return err
	}
	b := d.blobs[name]
	if b == nil || !b.exists && !b.dExists {
		return fmt.Errorf("sim: disk sync %s on %s: %w", name, h.n.id, env.ErrNotFound)
	}
	if !b.exists {
		delete(d.blobs, name)
		delete(d.damaged, name)
		return nil
	}
	b.dur = clone(b.data)
	if b.dur == nil {
		b.dur = []byte{}
	}
	b.dExists, b.dirty, b.appendOnly = true, false, false
	// Only Sync clears d.damaged (never unsynced Write or Delete), so a crash
	// before Sync preserves the damage mark on a still-rotted or wiped blob.
	if !b.rot {
		delete(d.damaged, name)
	}
	return nil
}

func (h *handle) Delete(name string) error {
	d, err := h.diskOp("delete", name)
	if err != nil {
		return err
	}
	b := d.blobs[name]
	if b == nil || !b.exists {
		return fmt.Errorf("sim: disk delete %s on %s: %w", name, h.n.id, env.ErrNotFound)
	}
	b.data, b.exists, b.dirty, b.appendOnly, b.rot = nil, false, true, false, false
	return nil
}

func (h *handle) List(prefix string) ([]string, error) {
	d, err := h.diskOp("list", prefix)
	if err != nil {
		return nil, err
	}
	return d.list(prefix), nil
}

func (d *Disk) list(prefix string) []string {
	out := []string{}
	for _, name := range sortedKeys(d.blobs) {
		if d.blobs[name].exists && strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out
}

// crash discards unsynced written-layer changes. When torn is true, any blob
// whose only unsynced edits are appends onto its durable content (or onto a
// newly created blob) retains a random prefix of the appended bytes from the
// disk PRNG stream.
func (d *Disk) crash(torn bool) {
	for _, name := range sortedKeys(d.blobs) {
		b := d.blobs[name]
		if b.dirty {
			if torn && b.appendOnly && b.exists && len(b.data) > len(b.dur) {
				keep := len(b.dur) + d.s.diskRand.IntN(len(b.data)-len(b.dur)+1)
				// Extend b.dur directly rather than slicing b.data so any
				// earlier bit-rot in b.dur is preserved.
				b.dur = append(clone(b.dur), b.data[len(b.dur):keep]...)
				if keep > 0 {
					b.dExists = true
				}
			}
			b.data, b.exists = clone(b.dur), b.dExists
			b.dirty, b.appendOnly, b.rot = false, false, d.damaged[name]
		}
		if !b.exists {
			delete(d.blobs, name)
		}
	}
}

// stored returns sorted names of blobs present in either the written or durable layer.
func (d *Disk) stored() []string {
	var out []string
	for _, name := range sortedKeys(d.blobs) {
		if b := d.blobs[name]; b.exists || b.dExists {
			out = append(out, name)
		}
	}
	return out
}

func (d *Disk) wipe() {
	for _, name := range d.stored() {
		d.damaged[name] = true
	}
	d.blobs = map[string]*blob{}
}

func (d *Disk) rotTargets() []string {
	var out []string
	for _, name := range sortedKeys(d.blobs) {
		b := d.blobs[name]
		if b.dExists && len(b.dur) > 0 {
			out = append(out, name)
		}
	}
	return out
}

func (d *Disk) rot(name string) error {
	if name == "" {
		targets := d.rotTargets()
		if len(targets) == 0 {
			return fmt.Errorf("sim: bit-rot: no durable blob: %w", env.ErrNotFound)
		}
		name = targets[d.s.diskRand.IntN(len(targets))]
	}
	b := d.blobs[name]
	if b == nil || !b.dExists {
		return fmt.Errorf("sim: bit-rot %s: %w", name, env.ErrNotFound)
	}
	if len(b.dur) == 0 {
		return fmt.Errorf("sim: bit-rot %s: empty blob: %w", name, env.ErrInvalidArgument)
	}
	bit := d.s.diskRand.IntN(len(b.dur) * 8)
	b.dur[bit/8] ^= 1 << (bit % 8)
	if !b.dirty {
		b.data[bit/8] ^= 1 << (bit % 8)
		b.rot = true
	}
	d.damaged[name] = true
	return nil
}

func (d *Disk) clearReadFaults() {
	d.failReads = map[string]int{}
	d.failAny = 0
}

func (d *Disk) affected() map[string]bool {
	hit := map[string]bool{}
	for _, name := range sortedKeys(d.damaged) {
		hit[name] = true
	}
	for _, name := range sortedKeys(d.failReads) {
		if d.failReads[name] > 0 {
			hit[name] = true
		}
	}
	if d.failAny > 0 {
		for _, name := range d.stored() {
			hit[name] = true
		}
	}
	return hit
}

func (d *Disk) faulty() bool {
	return len(d.damaged) > 0 || len(d.failReads) > 0 || d.failAny > 0
}

// Peek returns a copy of the blob's written layer without triggering read
// errors, for test assertions and invariants.
func (d *Disk) Peek(name string) ([]byte, bool) {
	b := d.blobs[name]
	if b == nil || !b.exists {
		return nil, false
	}
	return clone(b.data), true
}

// PeekDurable returns a copy of the blob's durable layer without triggering
// read errors, for test assertions and invariants.
func (d *Disk) PeekDurable(name string) ([]byte, bool) {
	b := d.blobs[name]
	if b == nil || !b.dExists {
		return nil, false
	}
	return clone(b.dur), true
}

// Names returns the sorted names of all blobs in the written layer.
func (d *Disk) Names() []string { return d.list("") }

// Damaged returns the sorted names of blobs whose durable copy currently
// carries un-repaired bit-rot or wipe damage.
func (d *Disk) Damaged() []string { return sortedKeys(d.damaged) }

func (d *Disk) encode(out []byte) []byte {
	names := sortedKeys(d.blobs)
	out = appendUvarint(out, uint64(len(names)))
	for _, name := range names {
		b := d.blobs[name]
		out = appendBytes(out, []byte(name))
		flags := byte(0)
		if b.exists {
			flags |= 1
		}
		if b.dExists {
			flags |= 2
		}
		if b.dirty {
			flags |= 4
		}
		if b.appendOnly {
			flags |= 8
		}
		out = append(out, flags)
		out = appendBytes(out, b.data)
		out = appendBytes(out, b.dur)
	}
	return out
}

// BitRot flips one bit drawn from the disk stream in a durable blob on node id.
// If name is empty, a non-empty durable blob is chosen from the disk stream.
// The corruption is silent on read and marks the blob in Disk.Damaged until
// repaired by a clean Sync or durable delete. Returns ErrUnknownNode,
// env.ErrNotFound (no durable blob), or env.ErrInvalidArgument (empty blob).
func (s *Sim) BitRot(id env.NodeID, name string) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: bit-rot: %w", err)
	}
	return n.disk.rot(name)
}

// WipeDisk removes both written and durable layers on node id and marks every
// previously stored blob as damaged until repaired by a clean Sync.
func (s *Sim) WipeDisk(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: wipe disk: %w", err)
	}
	n.disk.wipe()
	return nil
}

// FailReads schedules the next count reads on node id (of blob name, or of any
// blob when name is empty) to fail with env.ErrIO. Repeated calls accumulate
// with saturation at math.MaxInt; non-positive count returns env.ErrInvalidArgument.
func (s *Sim) FailReads(id env.NodeID, name string, count int) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: fail reads: %w", err)
	}
	if count <= 0 {
		return fmt.Errorf("sim: fail reads on %s: count %d: %w", id, count, env.ErrInvalidArgument)
	}
	if name == "" {
		n.disk.failAny = addSat(n.disk.failAny, count)
		return nil
	}
	n.disk.failReads[name] = addSat(n.disk.failReads[name], count)
	return nil
}

// addSat adds non-negative integers a and b, saturating at math.MaxInt.
func addSat(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
