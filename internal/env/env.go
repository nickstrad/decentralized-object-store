package env

import (
	"math/rand/v2"
	"strconv"
	"time"
)

type NodeID string

type Region string

type Time int64

func (t Time) Add(d time.Duration) Time { return t + Time(d) }

func (t Time) Sub(u Time) time.Duration { return time.Duration(t - u) }

func (t Time) String() string {
	return strconv.FormatInt(int64(t), 10) + "ns"
}

type Clock interface {
	Now() Time
	AfterFunc(d time.Duration, fn func()) Timer
}

type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

type Transport interface {
	Send(to NodeID, msg []byte) error
	Request(to NodeID, req []byte, timeout time.Duration, cb func(reply []byte, err error))
}

type Disk interface {
	Read(name string) ([]byte, error)
	Write(name string, data []byte) error
	Append(name string, data []byte) error
	Sync(name string) error
	Delete(name string) error
	List(prefix string) ([]string, error)
}

type Reply func(resp []byte, err error)

type Process interface {
	Start()
	OnDatagram(from NodeID, msg []byte)
	OnRequest(from NodeID, req []byte, reply Reply)
	EncodeState() []byte
}

type Env struct {
	ID     NodeID
	Region Region
	Clock  Clock
	Rand   *rand.Rand
	Net    Transport
	Disk   Disk
}

func (e Env) WithNet(t Transport) Env {
	e.Net = t
	return e
}

func (e Env) WithDisk(d Disk) Env {
	e.Disk = d
	return e
}

func (e Env) WithClock(c Clock) Env {
	e.Clock = c
	return e
}
