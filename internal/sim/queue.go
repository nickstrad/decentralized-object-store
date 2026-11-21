package sim

import (
	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Kind uint8

const (
	KindStart Kind = iota + 1
	KindDatagram
	KindRequest
	KindResponse
	KindRefused
	KindTimeout
	KindTimer
	KindLocal
	KindFault
	KindAction
)

var kindNames = [...]string{"?", "start", "datagram", "request", "response", "refused", "timeout", "timer", "local", "fault", "action"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "?"
}

type Disposition uint8

const (
	DispRan Disposition = iota
	DispStale
	DispDown
	DispHeld
	DispClogged
	DispRequeued
	DispRejected
	DispSkipped
	DispFailed
)

var dispNames = [...]string{"ran", "stale", "down", "held", "clogged", "requeued", "rejected", "skipped", "failed"}

func (d Disposition) String() string {
	if int(d) < len(dispNames) {
		return dispNames[d]
	}
	return "?"
}

type event struct {
	at       env.Time
	seq      uint64
	hi       int
	kind     Kind
	node     int
	peer     int
	inc      uint32
	peerInc  uint32
	payload  []byte
	call     uint64
	code     env.Code
	msg      string
	label    string
	fn       func()
	fault    *Fault
	nemesis  *Nemesis
	timer    *timer
	released bool
	relLink  bool
	dead     bool
	until    env.Time
}

type eventQueue []*event

func (q eventQueue) less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}

func (q eventQueue) swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].hi = i
	q[j].hi = j
}

func (q eventQueue) up(j int) {
	for j > 0 {
		i := (j - 1) / 2
		if !q.less(j, i) {
			break
		}
		q.swap(i, j)
		j = i
	}
}

func (q eventQueue) down(i0 int) bool {
	i := i0
	n := len(q)
	for {
		l := 2*i + 1
		if l >= n {
			break
		}
		j := l
		if r := l + 1; r < n && q.less(r, l) {
			j = r
		}
		if !q.less(j, i) {
			break
		}
		q.swap(i, j)
		i = j
	}
	return i > i0
}

func (q *eventQueue) push(e *event) {
	e.hi = len(*q)
	*q = append(*q, e)
	q.up(e.hi)
}

func (q *eventQueue) pop() *event {
	old := *q
	n := len(old) - 1
	old.swap(0, n)
	e := old[n]
	old[n] = nil
	*q = old[:n]
	if n > 0 {
		q.down(0)
	}
	e.hi = -1
	return e
}

func (q *eventQueue) remove(e *event) {
	i := e.hi
	if i < 0 || i >= len(*q) || (*q)[i] != e {
		return
	}
	old := *q
	n := len(old) - 1
	if i != n {
		old.swap(i, n)
	}
	old[n] = nil
	*q = old[:n]
	if i != n {
		if !q.down(i) {
			q.up(i)
		}
	}
	e.hi = -1
}

func (q eventQueue) peek() *event {
	if len(q) == 0 {
		return nil
	}
	return q[0]
}
