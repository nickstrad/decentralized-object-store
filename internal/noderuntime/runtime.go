// Package noderuntime hosts a single-threaded node.Node on real UDP/TCP
// sockets, a file-backed env.Disk, and wall-clock timers, and adapts it to
// httpapi.Service.
//
// All protocol state is owned by one loop goroutine; the runtime mutex guards
// only lifecycle state and the set of active TCP connections. Every goroutine
// is started through sync.WaitGroup.Go and exits before Shutdown returns. Peer
// traffic is unauthenticated and one TCP connection is used per request.
package noderuntime

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
)

// Sentinel errors returned by Runtime:
//   - ErrPanicked: returned by Err when the node loop recovered a panic and
//     stopped the runtime.
//   - ErrOutcomeUnknown: joined with the cancellation error when a service call
//     was canceled after the node loop already started running it.
var (
	ErrPanicked       = errors.New("node: node loop panicked")
	ErrOutcomeUnknown = errors.New("node: call abandoned after the node loop took it; outcome unknown")
)

// Default queue bounds, connection caps, and I/O timeouts for RuntimeConfig.
const (
	DefaultResolveTimeout = 5 * time.Second
	DefaultSendTimeout    = 100 * time.Millisecond
	DefaultDatagramQueue  = 1024
	DefaultControlQueue   = 4096
	DefaultMaxConns       = 256
	DefaultIOTimeout      = 10 * time.Second
	DefaultServeTimeout   = 30 * time.Second
)

// RuntimeConfig configures a Runtime and its embedded node.Node. Zero queue,
// connection, timeout, and Lookup fields use Default* values and
// net.DefaultResolver; an empty Listen defaults to ":<port>" from ID unless
// NoNetwork is true.
type RuntimeConfig struct {
	ID             env.NodeID
	Region         env.Region
	Listen         string
	DataDir        string
	Node           node.Config
	DatagramQueue  int
	ControlQueue   int
	MaxConns       int
	IOTimeout      time.Duration
	ServeTimeout   time.Duration
	NoNetwork      bool
	ResolveTimeout time.Duration
	SendTimeout    time.Duration
	Lookup         func(ctx context.Context, host string) ([]netip.Addr, error)
	Logf           func(format string, args ...any)
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
	if c.ResolveTimeout == 0 {
		c.ResolveTimeout = DefaultResolveTimeout
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.Lookup == nil {
		c.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if c.Listen == "" && !c.NoNetwork {
		if _, port, err := net.SplitHostPort(string(c.ID)); err == nil {
			c.Listen = ":" + port
		}
	}
	return c
}

// Validate checks c's identity, directory, bounds, network addresses (unless
// NoNetwork), and embedded Node configuration, returning node.ErrConfig on
// failure.
func (c RuntimeConfig) Validate() error {
	c = c.withDefaults()
	switch {
	case c.ID == "" || len(c.ID) > 255:
		return fmt.Errorf("node: runtime: id %q: %w", c.ID, node.ErrConfig)
	case len(c.Region) > 255:
		return fmt.Errorf("node: runtime: region of %d bytes: %w", len(c.Region), node.ErrConfig)
	case c.DataDir == "":
		return fmt.Errorf("node: runtime: no data directory: %w", node.ErrConfig)
	case c.DatagramQueue < 0 || c.ControlQueue < 1 || c.MaxConns < 1 || c.IOTimeout < 0 || c.ServeTimeout < 0 || c.ResolveTimeout < 0 || c.SendTimeout < 0:
		return fmt.Errorf("node: runtime: queue, connection or timeout bound out of range: %w", node.ErrConfig)
	}
	if !c.NoNetwork {
		if _, _, err := net.SplitHostPort(string(c.ID)); err != nil {
			return fmt.Errorf("node: runtime: id %q is not host:port: %w", c.ID, node.ErrConfig)
		}
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("node: runtime: listen %q: %w", c.Listen, node.ErrConfig)
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

// Runtime owns the real OS environment, event loop, sockets, and lifecycle of
// one node.Node. Callers must call Start before service methods and Shutdown or
// Close when done.
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

	dropped    atomic.Uint64
	refused    atomic.Uint64
	badFrames  atomic.Uint64
	panicked   atomic.Bool
	abandoned  atomic.Uint64
	unresolved atomic.Uint64

	halted   chan struct{}
	haltOnce sync.Once
	failure  error

	startHook   func()
	dequeueHook func(op string, left <-chan struct{})

	resolving map[env.NodeID]bool
	nd        *node.Node
	timers    map[*realTimer]struct{}
	pending   map[uint64]bool
	nextID    uint64
	addrs     map[env.NodeID]*net.UDPAddr
}

var canaryUntrackedGoroutine chan struct{}

// NewRuntime validates cfg and allocates an unstarted Runtime.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	codes, err := node.CodeTable()
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
		pending: map[uint64]bool{}, addrs: map[env.NodeID]*net.UDPAddr{}, resolving: map[env.NodeID]bool{},
		halted: make(chan struct{}),
	}, nil
}

func (rt *Runtime) logf(f string, a ...any) {
	if rt.cfg.Logf != nil {
		rt.cfg.Logf(f, a...)
	}
}

// ID returns the node's advertised env.NodeID.
func (rt *Runtime) ID() env.NodeID { return rt.cfg.ID }

// Addr returns the bound TCP listener address, or "" before Start or when
// NoNetwork is set.
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

// Start opens the data directory, binds UDP and TCP sockets (unless NoNetwork),
// launches the node loop and socket goroutines, and runs Node.Start. It returns
// env.ErrClosed if already closed, node.ErrConfig if called twice, or ctx.Err()
// if canceled during startup.
func (rt *Runtime) Start(ctx context.Context) error {
	rt.mu.Lock()
	switch rt.state {
	case stateClosed:
		rt.mu.Unlock()
		return fmt.Errorf("node: start %s: %w", rt.cfg.ID, env.ErrClosed)
	case stateNew:
	default:
		rt.mu.Unlock()
		return fmt.Errorf("node: start %s twice: %w", rt.cfg.ID, node.ErrConfig)
	}
	rt.state = stateStarting
	rt.mu.Unlock()
	if rt.startHook != nil {
		rt.startHook()
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, rt.Close())
	}
	if err := os.MkdirAll(rt.cfg.DataDir, 0o755); err != nil {
		return errors.Join(fmt.Errorf("node: start %s: data dir: %w", rt.cfg.ID, err), rt.Close())
	}
	disk, err := openFileDisk(osFS{}, rt.cfg.DataDir)
	if err != nil {
		return errors.Join(fmt.Errorf("node: start %s: %w", rt.cfg.ID, err), rt.Close())
	}
	e := env.Env{ID: rt.cfg.ID, Region: rt.cfg.Region, Clock: realClock{rt: rt}, Rand: newRand(), Net: &realNet{rt: rt}, Disk: disk}
	n, err := node.New(e, rt.cfg.Node)
	if err != nil {
		return errors.Join(err, rt.Close())
	}
	rt.nd = n
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
	if !rt.post(func() { rt.nd.Start(); close(ready) }) {
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

func (rt *Runtime) halt() { rt.haltOnce.Do(func() { close(rt.halted) }) }

func (rt *Runtime) closeSockets() []error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
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
	return errs
}

// loop runs all protocol callbacks, timers, and inbound datagrams on one
// goroutine. A recovered panic records ErrPanicked, cancels rt.ctx, closes all
// sockets so readers/acceptors exit without Close, and closes Done().
func (rt *Runtime) loop() {
	defer func() {
		if r := recover(); r != nil {
			rt.mu.Lock()
			rt.failure = fmt.Errorf("node %s: %v: %w", rt.cfg.ID, r, ErrPanicked)
			rt.mu.Unlock()
			rt.panicked.Store(true)
			rt.logf("node %s: stopped after a panic on the node loop: %v", rt.cfg.ID, r)
			// Cancel before closeSockets so any concurrent track() sees the
			// cancelled context and refuses newly accepted connections.
			rt.cancel()
			for _, err := range rt.closeSockets() {
				if err != nil && !errors.Is(err, net.ErrClosed) {
					rt.logf("node %s: close after panic: %v", rt.cfg.ID, err)
				}
			}
			rt.halt()
		}
	}()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case f := <-rt.ctrl:
			f()
		case d := <-rt.dgrams:
			rt.nd.OnDatagram(d.from, d.body)
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
	if rt.state == stateClosed || rt.ctx.Err() != nil || rt.panicked.Load() {
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

// serve handles one framed TCP request/response exchange on c within IOTimeout
// and ServeTimeout.
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
		rt.nd.OnRequest(req.From, req.Body, func(resp []byte, err error) {
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

var outboundFilter func(dataDir string, to env.NodeID) bool

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
	if outboundFilter != nil && outboundFilter(rt.cfg.DataDir, to) {
		return nil
	}
	b, err := encodeDatagram(rt.cfg.ID, msg)
	if err != nil {
		return err
	}
	// Resolve non-numeric hostnames asynchronously off the loop so a slow or
	// failing DNS lookup never stalls protocol timers or Shutdown.
	addr, ok := rt.addrs[to]
	if !ok {
		ap, perr := netip.ParseAddrPort(string(to))
		if perr != nil {
			rt.unresolved.Add(1)
			if err := rt.resolve(to); err != nil {
				return err
			}
			return fmt.Errorf("node: send to %s: address not resolved yet, datagram dropped: %w", to, env.ErrUnreachable)
		}
		addr = net.UDPAddrFromAddrPort(ap)
		rt.addrs[to] = addr
	}
	if err := rt.udp.SetWriteDeadline(time.Now().Add(rt.cfg.SendTimeout)); err != nil {
		return fmt.Errorf("node: send to %s: %v: %w", to, err, env.ErrUnreachable)
	}
	if _, err := rt.udp.WriteToUDP(b, addr); err != nil {
		delete(rt.addrs, to)
		return fmt.Errorf("node: send to %s: %v: %w", to, err, env.ErrUnreachable)
	}
	return nil
}

func (rt *Runtime) resolve(to env.NodeID) error {
	if rt.resolving[to] {
		return nil
	}
	host, port, err := net.SplitHostPort(string(to))
	if err != nil {
		return fmt.Errorf("node: send to %s: %v: %w", to, err, env.ErrUnreachable)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("node: send to %s: port: %v: %w", to, err, env.ErrUnreachable)
	}
	rt.resolving[to] = true
	lookup, timeout := rt.cfg.Lookup, rt.cfg.ResolveTimeout
	rt.wg.Go(func() {
		ctx, cancel := context.WithTimeout(rt.ctx, timeout)
		defer cancel()
		ips, err := lookup(ctx, host)
		rt.post(func() {
			delete(rt.resolving, to)
			if err != nil || len(ips) == 0 {
				rt.logf("node %s: resolve %s: %v", rt.cfg.ID, to, err)
				return
			}
			rt.addrs[to] = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ips[0].Unmap(), uint16(p)))
		})
	})
	return nil
}

// Request validates timeout and payload size before registering a pending ID or
// timer so cb is invoked at most once on every path while the loop runs.
func (t *realNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	rt := t.rt
	if timeout <= 0 {
		rt.afterFunc(0, func() {
			cb(nil, fmt.Errorf("node: request to %s with timeout %v: %w", to, timeout, env.ErrInvalidArgument))
		})
		return
	}
	if len(req) > node.MaxFrame {
		rt.afterFunc(0, func() {
			cb(nil, fmt.Errorf("node: request of %d bytes over %d: %w", len(req), node.MaxFrame, env.ErrTooLarge))
		})
		return
	}
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
			rt.nd.OnRequest(to, body, func(resp []byte, err error) {
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
	if outboundFilter != nil && outboundFilter(rt.cfg.DataDir, to) {
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
	// Dials happen asynchronously after scheduling and carry no destination
	// incarnation, so a queued request can still reach a restarted peer before
	// deadline even though an established TCP connection cannot survive restart.
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
		return nil, fmt.Errorf("node: response %d to request %d from %s: %w", f.ID, id, to, node.ErrFrame)
	}
	if f.Code != env.CodeOK {
		return nil, rt.codes.Decode(f.Code, f.Msg)
	}
	return f.Body, nil
}

// Shutdown gracefully stops the embedded Node within ctx, cancels the runtime
// context, closes all sockets and tracked connections, waits for every
// goroutine to exit, and stops pending timers. A second call waits for the
// first and returns nil.
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
		if rt.post(func() { rt.nd.Stop(); close(stopped) }) {
			select {
			case <-stopped:
			case <-ctx.Done():
				graceful = ctx.Err()
			case <-rt.ctx.Done():
			}
		}
	}
	rt.cancel()
	rt.halt()
	errs := rt.closeSockets()
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

// Close calls Shutdown with context.Background().
func (rt *Runtime) Close() error { return rt.Shutdown(context.Background()) }

// Diagnostic runtime counters and panic state:
//   - Dropped: inbound UDP datagrams dropped on a full queue.
//   - Refused: inbound TCP connections refused at MaxConns.
//   - BadFrames: malformed datagrams or TCP frames dropped.
//   - Panicked: reports whether the node loop exited by recovering a panic.
//   - Abandoned: service calls that finished after their caller left.
//   - Unresolved: outbound datagrams dropped while awaiting hostname resolution.
func (rt *Runtime) Dropped() uint64    { return rt.dropped.Load() }
func (rt *Runtime) Refused() uint64    { return rt.refused.Load() }
func (rt *Runtime) BadFrames() uint64  { return rt.badFrames.Load() }
func (rt *Runtime) Panicked() bool     { return rt.panicked.Load() }
func (rt *Runtime) Abandoned() uint64  { return rt.abandoned.Load() }
func (rt *Runtime) Unresolved() uint64 { return rt.unresolved.Load() }

// Done returns a channel closed when the runtime halts via Shutdown or a loop
// panic.
func (rt *Runtime) Done() <-chan struct{} { return rt.halted }

// Err returns the fatal loop error (wrapping ErrPanicked) if the loop panicked,
// or nil otherwise.
func (rt *Runtime) Err() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.failure
}
