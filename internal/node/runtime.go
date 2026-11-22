package node

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	DefaultDatagramQueue = 1024
	DefaultControlQueue  = 4096
	DefaultMaxConns      = 256
	DefaultIOTimeout     = 10 * time.Second
	DefaultServeTimeout  = 30 * time.Second
)

type RuntimeConfig struct {
	ID            env.NodeID
	Region        env.Region
	Listen        string
	DataDir       string
	Node          Config
	DatagramQueue int
	ControlQueue  int
	MaxConns      int
	IOTimeout     time.Duration
	ServeTimeout  time.Duration
	NoNetwork     bool
	Logf          func(format string, args ...any)
}

func (c RuntimeConfig) withDefaults() RuntimeConfig {
	if c.DatagramQueue == 0 {
		c.DatagramQueue = DefaultDatagramQueue
	}
	if c.ControlQueue == 0 {
		c.ControlQueue = DefaultControlQueue
	}
	if c.MaxConns == 0 {
		c.MaxConns = DefaultMaxConns
	}
	if c.IOTimeout == 0 {
		c.IOTimeout = DefaultIOTimeout
	}
	if c.ServeTimeout == 0 {
		c.ServeTimeout = DefaultServeTimeout
	}
	if c.Listen == "" && !c.NoNetwork {
		if _, port, err := net.SplitHostPort(string(c.ID)); err == nil {
			c.Listen = ":" + port
		}
	}
	return c
}

func (c RuntimeConfig) Validate() error {
	c = c.withDefaults()
	switch {
	case c.ID == "" || len(c.ID) > 255:
		return fmt.Errorf("node: runtime: id %q: %w", c.ID, ErrConfig)
	case len(c.Region) > 255:
		return fmt.Errorf("node: runtime: region of %d bytes: %w", len(c.Region), ErrConfig)
	case c.DataDir == "":
		return fmt.Errorf("node: runtime: no data directory: %w", ErrConfig)
	case c.DatagramQueue < 0 || c.ControlQueue < 1 || c.MaxConns < 1 || c.IOTimeout < 0 || c.ServeTimeout < 0:
		return fmt.Errorf("node: runtime: queue, connection or timeout bound out of range: %w", ErrConfig)
	}
	if !c.NoNetwork {
		if _, _, err := net.SplitHostPort(string(c.ID)); err != nil {
			return fmt.Errorf("node: runtime: id %q is not host:port: %w", c.ID, ErrConfig)
		}
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("node: runtime: listen %q: %w", c.Listen, ErrConfig)
		}
	}
	return c.Node.Validate()
}

const (
	stateNew = iota
	stateStarting
	stateRunning
	stateClosed
)

type inboundDatagram struct {
	from env.NodeID
	body []byte
}

type Runtime struct {
	cfg   RuntimeConfig
	codes *env.CodeTable

	mu      sync.Mutex
	state   int
	closeCh chan struct{}
	done    chan struct{}
	conns   map[net.Conn]struct{}
	udp     *net.UDPConn
	tcp     net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	ctrl    chan func()
	dgrams  chan inboundDatagram
	connSem chan struct{}

	dropped   atomic.Uint64
	refused   atomic.Uint64
	badFrames atomic.Uint64
	panicked  atomic.Bool

	node    *Node
	timers  map[*realTimer]struct{}
	pending map[uint64]bool
	nextID  uint64
	addrs   map[env.NodeID]*net.UDPAddr
}

var canaryUntrackedGoroutine chan struct{}

func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	codes, err := CodeTable()
	if err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		cfg: cfg, codes: codes, closeCh: make(chan struct{}), done: make(chan struct{}),
		conns: map[net.Conn]struct{}{}, ctx: ctx, cancel: cancel,
		ctrl: make(chan func(), cfg.ControlQueue), dgrams: make(chan inboundDatagram, cfg.DatagramQueue),
		connSem: make(chan struct{}, cfg.MaxConns), timers: map[*realTimer]struct{}{},
		pending: map[uint64]bool{}, addrs: map[env.NodeID]*net.UDPAddr{},
	}, nil
}

func (rt *Runtime) logf(f string, a ...any) {
	if rt.cfg.Logf != nil {
		rt.cfg.Logf(f, a...)
	}
}

func (rt *Runtime) ID() env.NodeID { return rt.cfg.ID }

func (rt *Runtime) Addr() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.tcp == nil {
		return ""
	}
	return rt.tcp.Addr().String()
}

func (rt *Runtime) post(f func()) bool {
	select {
	case rt.ctrl <- f:
		return true
	case <-rt.ctx.Done():
		return false
	}
}

func newRand() *rand.Rand {
	var seed [16]byte
	if _, err := crand.Read(seed[:]); err != nil {
		binary.LittleEndian.PutUint64(seed[:8], uint64(time.Now().UnixNano()))
	}
	return rand.New(rand.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])))
}

func (rt *Runtime) Start(ctx context.Context) error {
	rt.mu.Lock()
	switch rt.state {
	case stateClosed:
		rt.mu.Unlock()
		return fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed)
	case stateNew:
	default:
		rt.mu.Unlock()
		return fmt.Errorf("node: start %s twice: %w", rt.cfg.ID, ErrConfig)
	}
	rt.state = stateStarting
	rt.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return errors.Join(err, rt.Close())
	}
	if err := os.MkdirAll(rt.cfg.DataDir, 0o755); err != nil {
		return errors.Join(fmt.Errorf("node: start %s: data dir: %w", rt.cfg.ID, err), rt.Close())
	}
	e := env.Env{ID: rt.cfg.ID, Region: rt.cfg.Region, Clock: realClock{rt: rt}, Rand: newRand(), Net: &realNet{rt: rt}, Disk: &fileDisk{dir: rt.cfg.DataDir}}
	n, err := New(e, rt.cfg.Node)
	if err != nil {
		return errors.Join(err, rt.Close())
	}
	rt.node = n
	var udp *net.UDPConn
	var tcp net.Listener
	if !rt.cfg.NoNetwork {
		ua, err := net.ResolveUDPAddr("udp", rt.cfg.Listen)
		if err == nil {
			udp, err = net.ListenUDP("udp", ua)
		}
		if err == nil {
			var lc net.ListenConfig
			tcp, err = lc.Listen(ctx, "tcp", rt.cfg.Listen)
		}
		if err != nil {
			if udp != nil {
				err = errors.Join(err, udp.Close())
			}
			return errors.Join(fmt.Errorf("node: start %s: listen %s: %w", rt.cfg.ID, rt.cfg.Listen, err), rt.Close())
		}
	}
	rt.mu.Lock()
	if rt.state == stateClosed {
		rt.mu.Unlock()
		var errs []error
		if udp != nil {
			errs = append(errs, udp.Close(), tcp.Close())
		}
		return errors.Join(append(errs, fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed))...)
	}
	rt.udp, rt.tcp = udp, tcp
	rt.wg.Go(rt.loop)
	if udp != nil {
		rt.wg.Go(rt.readDatagrams)
		rt.wg.Go(rt.accept)
	}
	if canaryUntrackedGoroutine != nil {
		release := canaryUntrackedGoroutine
		go func() { <-release }()
	}
	rt.mu.Unlock()
	ready := make(chan struct{})
	if !rt.post(func() { rt.node.Start(); close(ready) }) {
		return fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		return errors.Join(ctx.Err(), rt.Close())
	case <-rt.closeCh:
		return fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.state == stateClosed {
		return fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed)
	}
	rt.state = stateRunning
	return nil
}

func (rt *Runtime) loop() {
	defer func() {
		if r := recover(); r != nil {
			rt.panicked.Store(true)
			rt.logf("node %s: stopped after a panic on the node loop: %v", rt.cfg.ID, r)
			rt.cancel()
		}
	}()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case f := <-rt.ctrl:
			f()
		case d := <-rt.dgrams:
			rt.node.OnDatagram(d.from, d.body)
		}
	}
}

func (rt *Runtime) readDatagrams() {
	buf := make([]byte, 1<<16)
	for {
		n, _, err := rt.udp.ReadFromUDP(buf)
		if err != nil {
			if rt.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		from, body, err := decodeDatagram(buf[:n])
		if err != nil {
			rt.badFrames.Add(1)
			continue
		}
		rt.enqueue(inboundDatagram{from: from, body: slices.Clone(body)})
	}
}

func (rt *Runtime) enqueue(d inboundDatagram) {
	select {
	case rt.dgrams <- d:
	default:
		rt.dropped.Add(1)
	}
}

func (rt *Runtime) track(c net.Conn) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.state == stateClosed {
		return false
	}
	rt.conns[c] = struct{}{}
	return true
}

func (rt *Runtime) untrack(c net.Conn) {
	rt.mu.Lock()
	delete(rt.conns, c)
	rt.mu.Unlock()
}

func (rt *Runtime) accept() {
	for {
		c, err := rt.tcp.Accept()
		if err != nil {
			if rt.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		select {
		case rt.connSem <- struct{}{}:
		default:
			rt.refused.Add(1)
			_ = c.Close()
			continue
		}
		if !rt.track(c) {
			_ = c.Close()
			<-rt.connSem
			return
		}
		rt.wg.Go(func() {
			defer func() {
				rt.untrack(c)
				_ = c.Close()
				<-rt.connSem
			}()
			rt.serve(c)
		})
	}
}

func (rt *Runtime) serve(c net.Conn) {
	if err := c.SetDeadline(time.Now().Add(rt.cfg.IOTimeout)); err != nil {
		return
	}
	payload, err := readFrame(c, MaxFrame)
	if err != nil {
		rt.badFrames.Add(1)
		return
	}
	req, err := decodeRequest(payload)
	if err != nil {
		rt.badFrames.Add(1)
		return
	}
	ch := make(chan responseFrame, 1)
	ok := rt.post(func() {
		replied := false
		rt.node.OnRequest(req.From, req.Body, func(resp []byte, err error) {
			if replied {
				return
			}
			replied = true
			code, msg := rt.codes.Encode(err)
			f := responseFrame{ID: req.ID, Code: code, Msg: msg}
			if code == env.CodeOK {
				f.Body = slices.Clone(resp)
			}
			ch <- f
		})
	})
	if !ok {
		return
	}
	wait := time.NewTimer(rt.cfg.ServeTimeout)
	defer wait.Stop()
	select {
	case f := <-ch:
		out, err := appendFrame(nil, encodeResponse(f))
		if err != nil {
			code, msg := rt.codes.Encode(err)
			out, _ = appendFrame(nil, encodeResponse(responseFrame{ID: req.ID, Code: code, Msg: msg}))
		}
		if err := c.SetWriteDeadline(time.Now().Add(rt.cfg.IOTimeout)); err != nil {
			return
		}
		if _, err := c.Write(out); err != nil {
			rt.badFrames.Add(1)
		}
	case <-rt.ctx.Done():
	case <-wait.C:
	}
}

type realNet struct{ rt *Runtime }

func (t *realNet) Send(to env.NodeID, msg []byte) error {
	rt := t.rt
	if rt.ctx.Err() != nil {
		return fmt.Errorf("node: send to %s: %w", to, env.ErrClosed)
	}
	if to == rt.cfg.ID {
		if len(msg) > MaxDatagram {
			return fmt.Errorf("node: datagram of %d bytes: %w", len(msg), env.ErrTooLarge)
		}
		rt.enqueue(inboundDatagram{from: to, body: slices.Clone(msg)})
		return nil
	}
	if rt.udp == nil {
		return fmt.Errorf("node: send to %s: no network: %w", to, env.ErrUnreachable)
	}
	b, err := encodeDatagram(rt.cfg.ID, msg)
	if err != nil {
		return err
	}
	addr, ok := rt.addrs[to]
	if !ok {
		addr, err = net.ResolveUDPAddr("udp", string(to))
		if err != nil {
			return fmt.Errorf("node: send to %s: %v: %w", to, err, env.ErrUnreachable)
		}
		rt.addrs[to] = addr
	}
	if _, err := rt.udp.WriteToUDP(b, addr); err != nil {
		delete(rt.addrs, to)
		return fmt.Errorf("node: send to %s: %v: %w", to, err, env.ErrUnreachable)
	}
	return nil
}

func (t *realNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	rt := t.rt
	rt.nextID++
	id := rt.nextID
	rt.pending[id] = true
	var tm *realTimer
	complete := func(resp []byte, err error) {
		if !rt.pending[id] {
			return
		}
		delete(rt.pending, id)
		if tm != nil {
			tm.Stop()
		}
		cb(resp, err)
	}
	tm = rt.afterFunc(timeout, func() {
		complete(nil, fmt.Errorf("node: request %d to %s after %v: %w", id, to, timeout, env.ErrTimeout))
	})
	if to == rt.cfg.ID {
		body := slices.Clone(req)
		rt.afterFunc(0, func() {
			rt.node.OnRequest(to, body, func(resp []byte, err error) {
				r := slices.Clone(resp)
				if err != nil {
					r, err = nil, rt.codes.Decode(rt.codes.Encode(err))
				}
				rt.afterFunc(0, func() { complete(r, err) })
			})
		})
		return
	}
	if rt.udp == nil {
		rt.afterFunc(0, func() { complete(nil, fmt.Errorf("node: request to %s: no network: %w", to, env.ErrUnreachable)) })
		return
	}
	payload, err := encodeRequest(requestFrame{ID: id, From: rt.cfg.ID, Body: req})
	var frame []byte
	if err == nil {
		frame, err = appendFrame(nil, payload)
	}
	if err != nil {
		rt.afterFunc(0, func() { complete(nil, err) })
		return
	}
	deadline := time.Now().Add(timeout)
	rt.wg.Go(func() {
		resp, err := rt.roundTrip(to, id, frame, deadline)
		rt.post(func() { complete(resp, err) })
	})
}

func (rt *Runtime) roundTrip(to env.NodeID, id uint64, frame []byte, deadline time.Time) ([]byte, error) {
	ctx, cancel := context.WithDeadline(rt.ctx, deadline)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", string(to))
	if err != nil {
		switch {
		case rt.ctx.Err() != nil:
			return nil, fmt.Errorf("node: request to %s: %w", to, env.ErrClosed)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("node: dial %s: %w", to, env.ErrTimeout)
		}
		return nil, fmt.Errorf("node: dial %s: %v: %w", to, err, env.ErrUnreachable)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer func() {
		stop()
		_ = c.Close()
	}()
	if err := c.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("node: request to %s: %v: %w", to, err, env.ErrUnreachable)
	}
	if _, err := c.Write(frame); err != nil {
		return nil, fmt.Errorf("node: request to %s: write: %v: %w", to, err, env.ErrTimeout)
	}
	payload, err := readFrame(c, MaxFrame)
	if err != nil {
		if errors.Is(err, env.ErrTooLarge) {
			return nil, err
		}
		return nil, fmt.Errorf("node: request to %s: read: %v: %w", to, err, env.ErrTimeout)
	}
	f, err := decodeResponse(payload)
	if err != nil {
		return nil, err
	}
	if f.ID != id {
		return nil, fmt.Errorf("node: response %d to request %d from %s: %w", f.ID, id, to, ErrFrame)
	}
	if f.Code != env.CodeOK {
		return nil, rt.codes.Decode(f.Code, f.Msg)
	}
	return f.Body, nil
}

func (rt *Runtime) Shutdown(ctx context.Context) error {
	rt.mu.Lock()
	if rt.state == stateClosed {
		rt.mu.Unlock()
		<-rt.done
		return nil
	}
	prev := rt.state
	rt.state = stateClosed
	close(rt.closeCh)
	rt.mu.Unlock()
	var graceful error
	if prev == stateRunning {
		stopped := make(chan struct{})
		if rt.post(func() { rt.node.Stop(); close(stopped) }) {
			select {
			case <-stopped:
			case <-ctx.Done():
				graceful = ctx.Err()
			case <-rt.ctx.Done():
			}
		}
	}
	rt.cancel()
	rt.mu.Lock()
	var errs []error
	if rt.udp != nil {
		errs = append(errs, rt.udp.Close())
	}
	if rt.tcp != nil {
		errs = append(errs, rt.tcp.Close())
	}
	for c := range rt.conns {
		errs = append(errs, c.Close())
	}
	rt.mu.Unlock()
	rt.wg.Wait()
	for tm := range rt.timers {
		if tm.t != nil {
			tm.t.Stop()
		}
	}
	close(rt.done)
	for _, err := range errs {
		if err != nil && !errors.Is(err, net.ErrClosed) {
			rt.logf("node %s: close: %v", rt.cfg.ID, err)
		}
	}
	return graceful
}

func (rt *Runtime) Close() error { return rt.Shutdown(context.Background()) }

func (rt *Runtime) Dropped() uint64   { return rt.dropped.Load() }
func (rt *Runtime) Refused() uint64   { return rt.refused.Load() }
func (rt *Runtime) BadFrames() uint64 { return rt.badFrames.Load() }
func (rt *Runtime) Panicked() bool    { return rt.panicked.Load() }
