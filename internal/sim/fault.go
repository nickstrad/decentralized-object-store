package sim

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type FaultKind uint8

const (
	FaultCrash FaultKind = iota + 1
	FaultCrashTorn
	FaultRestart
	FaultPause
	FaultResume
	FaultWipeRejoin
	FaultPartition
	FaultPartitionOneWay
	FaultIsolate
	FaultPartitionRegion
	FaultLinkLoss
	FaultClog
	FaultUnclog
	FaultHealNet
	FaultHealAll
	FaultBitRot
	FaultWipeDisk
	FaultReadError
	faultKindEnd
)

var faultNames = [...]string{"?", "crash", "crash-torn", "restart", "pause", "resume", "wipe-rejoin",
	"partition", "partition-one-way", "isolate", "partition-region", "link-loss", "clog", "unclog",
	"heal-net", "heal-all", "bit-rot", "wipe-disk", "read-error"}

func (k FaultKind) String() string {
	if k > 0 && k < faultKindEnd {
		return faultNames[k]
	}
	return fmt.Sprintf("fault(%d)", uint8(k))
}

func (k FaultKind) valid() bool { return k > 0 && k < faultKindEnd }

func ChaosKinds() []FaultKind {
	return []FaultKind{FaultCrash, FaultCrashTorn, FaultRestart, FaultPause, FaultResume, FaultWipeRejoin,
		FaultPartition, FaultPartitionOneWay, FaultIsolate, FaultPartitionRegion, FaultLinkLoss, FaultClog,
		FaultUnclog, FaultBitRot, FaultWipeDisk, FaultReadError}
}

type Fault struct {
	Kind   FaultKind
	Node   env.NodeID
	Peer   env.NodeID
	Side   []env.NodeID
	Other  []env.NodeID
	Region env.Region
	Blob   string
	PPM    uint32
	Count  int
}

type TimedFault struct {
	At    env.Time
	Fault Fault
}

func appendUvarint(out []byte, v uint64) []byte { return binary.AppendUvarint(out, v) }

func appendBytes(out []byte, b []byte) []byte {
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

func appendIDs(out []byte, ids []env.NodeID) []byte {
	out = binary.AppendUvarint(out, uint64(len(ids)))
	for _, id := range ids {
		out = appendBytes(out, []byte(id))
	}
	return out
}

func (f Fault) Encode() []byte {
	out := []byte{byte(f.Kind)}
	out = appendBytes(out, []byte(f.Node))
	out = appendBytes(out, []byte(f.Peer))
	out = appendIDs(out, f.Side)
	out = appendIDs(out, f.Other)
	out = appendBytes(out, []byte(f.Region))
	out = appendBytes(out, []byte(f.Blob))
	out = binary.AppendUvarint(out, uint64(f.PPM))
	out = binary.AppendVarint(out, int64(f.Count))
	return out
}

func joinIDs(ids []env.NodeID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func (f Fault) String() string {
	switch f.Kind {
	case FaultCrash, FaultCrashTorn, FaultRestart, FaultPause, FaultResume, FaultWipeRejoin, FaultIsolate, FaultWipeDisk:
		return f.Kind.String() + " " + string(f.Node)
	case FaultPartition, FaultPartitionOneWay:
		other := "rest"
		if len(f.Other) > 0 {
			other = joinIDs(f.Other)
		}
		return f.Kind.String() + " " + joinIDs(f.Side) + " " + other
	case FaultPartitionRegion:
		return f.Kind.String() + " " + string(f.Region)
	case FaultLinkLoss:
		return fmt.Sprintf("%s %s->%s %dppm", f.Kind, f.Node, f.Peer, f.PPM)
	case FaultClog, FaultUnclog:
		return fmt.Sprintf("%s %s->%s", f.Kind, f.Node, f.Peer)
	case FaultBitRot:
		return fmt.Sprintf("%s %s %q", f.Kind, f.Node, f.Blob)
	case FaultReadError:
		return fmt.Sprintf("%s %s %q x%d", f.Kind, f.Node, f.Blob, f.Count)
	default:
		return f.Kind.String()
	}
}

func (s *Sim) Apply(f Fault) error {
	switch f.Kind {
	case FaultCrash:
		return s.Crash(f.Node)
	case FaultCrashTorn:
		return s.CrashTorn(f.Node)
	case FaultRestart:
		return s.Restart(f.Node)
	case FaultPause:
		return s.Pause(f.Node)
	case FaultResume:
		return s.Resume(f.Node)
	case FaultWipeRejoin:
		return s.WipeRejoin(f.Node)
	case FaultPartition:
		return s.Partition(f.Side, f.Other)
	case FaultPartitionOneWay:
		return s.PartitionOneWay(f.Side, f.Other)
	case FaultIsolate:
		return s.Isolate(f.Node)
	case FaultPartitionRegion:
		return s.PartitionRegion(f.Region)
	case FaultLinkLoss:
		return s.SetLinkLoss(f.Node, f.Peer, f.PPM)
	case FaultClog:
		return s.Clog(f.Node, f.Peer)
	case FaultUnclog:
		return s.Unclog(f.Node, f.Peer)
	case FaultHealNet:
		s.HealNet()
		return nil
	case FaultHealAll:
		s.HealAll()
		return nil
	case FaultBitRot:
		return s.BitRot(f.Node, f.Blob)
	case FaultWipeDisk:
		return s.WipeDisk(f.Node)
	case FaultReadError:
		return s.FailReads(f.Node, f.Blob, f.Count)
	}
	return fmt.Errorf("sim: apply %v: %w", f.Kind, env.ErrInvalidArgument)
}

func (s *Sim) InjectAt(t env.Time, f Fault) {
	fc := f
	s.schedule(&event{at: t, kind: kindFault, node: -1, peer: -1, fault: &fc})
}

func (s *Sim) InjectSchedule(sched []TimedFault) env.Time {
	last := s.now
	for _, tf := range sched {
		s.InjectAt(tf.At, tf.Fault)
		if tf.At > last {
			last = tf.At
		}
	}
	return last
}

func (s *Sim) ScheduleFromBytes(data []byte, start env.Time, step time.Duration) []TimedFault {
	ids := s.Nodes()
	regions := s.Regions()
	if len(ids) == 0 {
		return nil
	}
	kinds := append(ChaosKinds(), FaultHealNet, FaultHealAll)
	var out []TimedFault
	at := start
	for i := 0; i+4 <= len(data); i += 4 {
		b0, b1, b2, b3 := data[i], data[i+1], data[i+2], data[i+3]
		at = later(at, scaled(int64(b0%16), step))
		f := Fault{Kind: kinds[int(b1)%len(kinds)]}
		f.Node = ids[int(b2)%len(ids)]
		f.Peer = ids[int(b3)%len(ids)]
		f.PPM = uint32(b3) * (PPM / 255)
		f.Count = 1 + int(b3%4)
		if len(regions) > 0 {
			f.Region = regions[int(b2)%len(regions)]
		}
		if f.Kind == FaultPartition || f.Kind == FaultPartitionOneWay {
			for j, id := range ids {
				if b3>>(j%8)&1 == 1 {
					f.Side = append(f.Side, id)
				}
			}
			if len(f.Side) == 0 {
				f.Side = []env.NodeID{f.Node}
			}
		}
		out = append(out, TimedFault{At: at, Fault: f})
	}
	return out
}
