// Package metrics defines framework-free, owner-confined cumulative observations.
package metrics

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type Kind uint8

const (
	Counter Kind = iota + 1
	Gauge
)
const (
	SchemaVersion    uint32 = 1
	MaxPoints               = 2048
	MaxBatchBytes           = 1 << 20
	MaxIdentityBytes        = 256
)

type Label struct{ Name, Value string }
type Descriptor struct {
	Name, Help, Unit string
	Kind             Kind
	LabelNames       []string
}
type Point struct {
	Name    string
	Labels  []Label
	Counter uint64
	Gauge   uint64
}
type Snapshot struct {
	Component string
	Points    []Point
}
type Source interface{ Snapshot() Snapshot }
type Batch struct {
	SchemaVersion               uint32
	NodeID, Region, BootID      string
	Sequence                    uint64
	StartUnixNano, TimeUnixNano int64
	Snapshots                   []Snapshot
}

func Catalogue() []Descriptor {
	out := slices.Clone(catalogue)
	for i := range out {
		out[i].LabelNames = slices.Clone(out[i].LabelNames)
	}
	slices.SortFunc(out, func(a, b Descriptor) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Clone severs every mutable slice in a batch before crossing an ownership boundary.
func Clone(b Batch) Batch {
	b.Snapshots = slices.Clone(b.Snapshots)
	for i := range b.Snapshots {
		b.Snapshots[i].Points = slices.Clone(b.Snapshots[i].Points)
		for j := range b.Snapshots[i].Points {
			b.Snapshots[i].Points[j].Labels = slices.Clone(b.Snapshots[i].Points[j].Labels)
		}
	}
	return b
}

func Validate(b Batch) error {
	if b.SchemaVersion != SchemaVersion || b.Sequence == 0 || b.StartUnixNano <= 0 || b.TimeUnixNano < b.StartUnixNano {
		return fmt.Errorf("invalid batch metadata")
	}
	for _, s := range []string{b.NodeID, b.Region, b.BootID} {
		if s == "" || len(s) > MaxIdentityBytes || !utf8.ValidString(s) {
			return fmt.Errorf("invalid batch identity")
		}
	}
	desc := make(map[string]Descriptor, len(catalogue))
	components := make(map[string]bool)
	for _, d := range catalogue {
		desc[d.Name] = d
		components[strings.SplitN(strings.TrimPrefix(d.Name, "objstore_"), "_", 2)[0]] = true
	}
	seenComponents := make(map[string]bool)
	seen := make(map[string]bool)
	count := 0
	// Conservative JSON bound: strings can expand sixfold under escaping.
	encodedBytes := 256 + 6*(len(b.NodeID)+len(b.Region)+len(b.BootID))
	for _, s := range b.Snapshots {
		if !components[s.Component] || seenComponents[s.Component] {
			return fmt.Errorf("unknown or duplicate component %q", s.Component)
		}
		seenComponents[s.Component] = true
		encodedBytes += 64 + 6*len(s.Component)
		for _, p := range s.Points {
			count++
			encodedBytes += 128 + 6*len(p.Name)
			for _, l := range p.Labels {
				encodedBytes += 32 + 6*(len(l.Name)+len(l.Value))
			}
			if encodedBytes > MaxBatchBytes {
				return fmt.Errorf("batch exceeds byte bound")
			}
			if count > MaxPoints {
				return fmt.Errorf("too many points")
			}
			d, ok := desc[p.Name]
			if !ok || !strings.HasPrefix(p.Name, "objstore_"+s.Component+"_") {
				return fmt.Errorf("unknown metric %q in %q", p.Name, s.Component)
			}
			if d.Kind == Counter && p.Gauge != 0 || d.Kind == Gauge && p.Counter != 0 {
				return fmt.Errorf("wrong kind value for %q", p.Name)
			}
			if len(p.Labels) != len(d.LabelNames) {
				return fmt.Errorf("wrong labels for %q", p.Name)
			}
			ls := slices.Clone(p.Labels)
			slices.SortFunc(ls, func(a, b Label) int { return strings.Compare(a.Name, b.Name) })
			key := p.Name
			for i, l := range ls {
				if l.Name != d.LabelNames[i] || !slices.Contains(labelDomains[p.Name][l.Name], l.Value) {
					return fmt.Errorf("invalid label for %q", p.Name)
				}
				key += "\x00" + l.Name + "=" + l.Value
			}
			if seen[key] {
				return fmt.Errorf("duplicate series %q", p.Name)
			}
			seen[key] = true
		}
	}
	return nil
}
