package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/noderuntime"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"time"
)

func realNode() (*noderuntime.Runtime, func()) {
	return realNodeWithTune(nil)
}
func realNodeWithTune(tune func(*node.Config)) (*noderuntime.Runtime, func()) {
	dir, e := os.MkdirTemp(".", "runtime-")
	must(e)
	cfg := config()
	cfg.RequestTimeout = 5 * time.Second
	cfg.Layout = storage.Layout{K: 1, M: 0, StripeSize: 1024}
	if tune != nil {
		tune(&cfg)
	}
	rt, e := noderuntime.NewRuntime(noderuntime.RuntimeConfig{ID: "127.0.0.1:1", Region: "r0", Listen: "127.0.0.1:0", DataDir: dir, Node: cfg, IOTimeout: 200 * time.Millisecond, ServeTimeout: 300 * time.Millisecond})
	must(e)
	must(rt.Start(context.Background()))
	return rt, func() { must(rt.Close()); must(os.RemoveAll(dir)) }
}
func req(client *http.Client, base, method, path, body, etag string) (int, http.Header, []byte) {
	r, e := http.NewRequest(method, base+path, strings.NewReader(body))
	must(e)
	if etag != "" {
		r.Header.Set("If-Match", etag)
	}
	res, e := client.Do(r)
	must(e)
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	must(e)
	return res.StatusCode, res.Header, b
}
func realChecks() {
	check("C1", "wire-codes", 0, func() string {
		t, e := node.CodeTable()
		must(e)
		seen := map[env.Code]bool{}
		for _, r := range t.Entries() {
			require(!seen[r.Code], "duplicate code=%d", r.Code)
			seen[r.Code] = true
			code, msg := t.Encode(fmt.Errorf("wrapped: %w", r.Err))
			require(code == r.Code && errors.Is(t.Decode(code, msg), r.Err), "round trip=%d", code)
		}
		for i := 0; i < 256; i++ {
			if !seen[env.Code(i)] && i != 0 {
				require(errors.Is(t.Decode(env.Code(i), "unknown"), env.ErrRemote), "unknown=%d", i)
			}
		}
		return "all registered sentinels round-trip wrapped; every unassigned byte maps to remote error"
	})
	check("C15", "frame-capacity", 0, func() string {
		require(merkle.MaxReplyBytes+1024 < node.MaxFrame, "merkle frame too small")
		for _, l := range []storage.Layout{{K: 2, M: 1, StripeSize: 1024}, storage.DefaultLayout()} {
			require(node.ShardRequestSize(l) < node.MaxFrame, "shard too large")
			must((node.Config{Layout: l}).Validate())
		}
		require((node.Config{MaxObjectSize: -1}).Validate() != nil, "negative max accepted")
		return "default and small layouts fit; Merkle reply fits; invalid config refused"
	})
	check("C16,C17,C27", "http-conditional-races", 0, func() string {
		rt, closeRT := realNode()
		defer closeRT()
		s := httpapi.NewServer("127.0.0.1:0", rt, httpapi.Limits{MaxObjectSize: 4096, MaxSmallBody: 64}, node.StatusRows()...)
		must(s.Start(context.Background()))
		defer s.Shutdown(context.Background())
		tr := &http.Transport{}
		defer tr.CloseIdleConnections()
		cl := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		base := "http://" + s.Addr()
		status, _, _ := req(cl, base, "PUT", "/b", "", "")
		require(status == 201 || status == 200, "bucket status=%d", status)
		unavailable := 0
		for round := range 8 {
			status, h, _ := req(cl, base, "PUT", "/b/k", "initial", "")
			require(status == 200 || status == 201, "put=%d", status)
			etag := h.Get("ETag")
			require(etag != "", "missing etag")
			type result struct {
				status int
				body   string
			}
			out := make(chan result, 12)
			var wg sync.WaitGroup
			for j := range 12 {
				wg.Go(func() {
					body := fmt.Sprintf("round%d-writer%d", round, j)
					r, e := http.NewRequest("PUT", base+"/b/k", strings.NewReader(body))
					if e != nil {
						out <- result{-1, e.Error()}
						return
					}
					r.Header.Set("If-Match", etag)
					res, e := cl.Do(r)
					if e != nil {
						out <- result{-1, e.Error()}
						return
					}
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
					out <- result{res.StatusCode, body}
				})
			}
			wg.Wait()
			close(out)
			wins := 0
			winner := ""
			for r := range out {
				if r.status == 200 || r.status == 201 {
					wins++
					winner = r.body
				} else {
					require(r.status == 412 || r.status == 503, "race status=%d body=%s", r.status, r.body)
					if r.status == 503 {
						unavailable++
					}
				}
			}
			require(wins == 1, "round=%d successes=%d", round, wins)
			status, _, b := req(cl, base, "GET", "/b/k", "", "")
			require(status == 200 && string(b) == winner, "get=%d bytes=%q winner=%q", status, b, winner)
			status, _, _ = req(cl, base, "DELETE", "/b/k", "", etag)
			require(status == 412, "stale delete=%d", status)
		}
		for _, v := range []struct {
			method, path, body string
			want               int
		}{{"PUT", "/b/too", strings.Repeat("x", 4097), 413}, {"PUT", "/b?quota", "{", 400}, {"POST", "/_migrations", "{", 400}, {"GET", "/missing/key", "", 404}, {"PATCH", "/b/k", "", 405}, {"PUT", "/b/k?tag=x", strings.Repeat("x", 65), 413}} {
			st, _, _ := req(cl, base, v.method, v.path, v.body, "")
			require(st == v.want, "%s %s status=%d want=%d", v.method, v.path, st, v.want)
		}
		return fmt.Sprintf("8 races x 12 writers: one success each, exact winning bytes, stale DELETE refused; unavailable=%d; six malformed/status checks", unavailable)
	})
	check("C18", "slow-and-large-http", 0, func() string {
		rt, closeRT := realNode()
		defer closeRT()
		s := httpapi.NewServer("127.0.0.1:0", rt, httpapi.Limits{MaxHeaderBytes: 1024, ReadHeaderTimeout: 80 * time.Millisecond, ReadTimeout: 120 * time.Millisecond}, node.StatusRows()...)
		must(s.Start(context.Background()))
		defer s.Shutdown(context.Background())
		for _, raw := range []string{"GET /_health HTTP/1.1\r\nHost: x\r\nX: ", "PUT /b/k HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nx"} {
			conn, e := net.Dial("tcp", s.Addr())
			must(e)
			must(conn.SetDeadline(time.Now().Add(2 * time.Second)))
			_, e = io.WriteString(conn, raw)
			must(e)
			start := time.Now()
			buf := make([]byte, 4096)
			_, e = conn.Read(buf)
			conn.Close()
			require(time.Since(start) < time.Second, "slow client not cut off: %v", e)
		}
		conn, e := net.Dial("tcp", s.Addr())
		must(e)
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprintf(conn, "GET /_health HTTP/1.1\r\nHost: x\r\nX: %s\r\n\r\n", strings.Repeat("a", 10000))
		res, e := http.ReadResponse(bufio.NewReader(conn), nil)
		must(e)
		defer res.Body.Close()
		require(res.StatusCode == 431, "large header=%d", res.StatusCode)
		return "partial headers and bodies terminated within one second; oversized header got 431"
	})
	check("C14,C28", "tcp-fragments-resets", 0, func() string {
		rt, closeRT := realNode()
		defer closeRT()
		payload := []byte{1, 1, 0, 0, 0, 0, 0, 0, 0, 7, 1, 'x', 255}
		frame := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
		frame = append(frame, payload...)
		conn, e := net.Dial("tcp", rt.Addr())
		must(e)
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		for _, b := range frame {
			_, e = conn.Write([]byte{b})
			must(e)
		}
		var hdr [4]byte
		_, e = io.ReadFull(conn, hdr[:])
		must(e)
		n := binary.BigEndian.Uint32(hdr[:])
		require(n < 1024, "response length=%d", n)
		reply := make([]byte, n)
		_, e = io.ReadFull(conn, reply)
		must(e)
		conn.Close()
		require(len(reply) >= 13 && reply[10] != 0, "invalid channel not rejected")
		for i := range len(frame) {
			c, e := net.Dial("tcp", rt.Addr())
			must(e)
			c.Write(frame[:i])
			c.(*net.TCPConn).SetLinger(0)
			c.Close()
		}
		for _, b := range [][]byte{{255, 255, 255, 255}, {0, 0, 0, 0}, {0, 0, 0, 2, 9, 9}} {
			c, e := net.Dial("tcp", rt.Addr())
			must(e)
			c.SetDeadline(time.Now().Add(time.Second))
			c.Write(b)
			var x [1]byte
			_, e = c.Read(x[:])
			require(e != nil, "malformed frame accepted")
			c.Close()
		}
		time.Sleep(250 * time.Millisecond)
		_, e = rt.Health(context.Background())
		must(e)
		require(!rt.Panicked() && rt.BadFrames() > 0, "runtime failed/no malformed frames")
		return fmt.Sprintf("bytewise valid frame rejected invalid channel; %d reset prefixes and three malformed lengths; runtime alive", len(frame))
	})
	check("C19,C20,C21,C22", "shutdown-churn", 0, func() string {
		base := runtime.NumGoroutine()
		fds, e := os.ReadDir("/proc/self/fd")
		must(e)
		for cycle := range 10 {
			rt, cleanup := realNode()
			ctx := context.Background()
			must(rt.CreateBucket(ctx, storage.BucketSpec{Name: "b"}))
			require(rt.Start(ctx) != nil, "double start accepted")
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, _, e := rt.GetObject(cancelled, "b", "k")
			require(errors.Is(e, context.Canceled), "cancel get=%v", e)
			var wg sync.WaitGroup
			for j := range 16 {
				wg.Go(func() {
					cx, stop := context.WithTimeout(ctx, time.Second)
					defer stop()
					rt.PutObject(cx, storage.PutRequest{Bucket: "b", Key: fmt.Sprint(j), Data: bytes.Repeat([]byte{byte(cycle)}, 2049)})
					rt.GetObject(cx, "b", fmt.Sprint(j))
				})
			}
			wg.Go(func() { rt.Close() })
			wg.Wait()
			cleanup()
			_, e = rt.Health(ctx)
			require(errors.Is(e, env.ErrClosed), "post-close=%v", e)
		}
		time.Sleep(600 * time.Millisecond)
		runtime.GC()
		after, e := os.ReadDir("/proc/self/fd")
		must(e)
		var stacks bytes.Buffer
		pprof.Lookup("goroutine").WriteTo(&stacks, 2)
		require(!strings.Contains(stacks.String(), "internal/noderuntime.(*Runtime)."), "runtime goroutine survives")
		require(len(after) <= len(fds) && runtime.NumGoroutine() <= base, "leak fd %d->%d goroutines %d->%d", len(fds), len(after), base, runtime.NumGoroutine())
		return "10 runtime cycles, 160 overlapping PUT/GET calls, exact FD and goroutine baseline restored"
	})
}
