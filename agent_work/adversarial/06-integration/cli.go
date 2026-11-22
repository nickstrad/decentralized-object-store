package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func freeAddr() string {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	must(e)
	a := l.Addr().String()
	must(l.Close())
	return a
}
func cliChecks() {
	check("C24", "three-cli-peers", 0, func() string {
		dir, e := os.MkdirTemp(".", "cli-")
		must(e)
		defer os.RemoveAll(dir)
		var cmds []*exec.Cmd
		var logs []*bytes.Buffer
		defer func() {
			for _, c := range cmds {
				if c.ProcessState == nil {
					c.Process.Kill()
					c.Wait()
				}
			}
		}()
		ids := []string{freeAddr(), freeAddr(), freeAddr()}
		addrs := []string{freeAddr(), freeAddr(), freeAddr()}
		cl := &http.Client{Timeout: time.Second}
		defer cl.CloseIdleConnections()
		for i := range 3 {
			log := &bytes.Buffer{}
			logs = append(logs, log)
			c := exec.Command("./evidence/objnode", "-id", ids[i], "-listen", ids[i], "-http", addrs[i], "-region", fmt.Sprintf("r%d", i), "-data", filepath.Join(dir, fmt.Sprint(i)), "-seeds", ids[0]+","+ids[1]+","+ids[2], "-k", "2", "-m", "1", "-stripe", "1024")
			c.Stdout = log
			c.Stderr = log
			must(c.Start())
			cmds = append(cmds, c)
		}
		deadline := time.Now().Add(12 * time.Second)
		for _, addr := range addrs {
			for {
				r, e := cl.Get("http://" + addr + "/_health")
				if e == nil {
					r.Body.Close()
					if r.StatusCode == 200 {
						break
					}
				}
				require(time.Now().Before(deadline), "CLI startup timed out")
				time.Sleep(50 * time.Millisecond)
			}
		}
		time.Sleep(2 * time.Second)
		base := "http://" + addrs[0]
		st, _, _ := req(cl, base, "PUT", "/b", "", "")
		require(st == 200 || st == 201, "CLI create=%d", st)
		st, h, _ := req(cl, base, "PUT", "/b/k", "cli-object", "")
		require(st == 200 || st == 201, "CLI put=%d", st)
		etag := h.Get("ETag")
		for i := range 3 {
			deadline := time.Now().Add(12 * time.Second)
			for {
				st, _, b := req(cl, "http://"+addrs[i], "GET", "/b/k", "", "")
				if st == 200 {
					require(string(b) == "cli-object", "CLI wrong bytes")
					break
				}
				require(time.Now().Before(deadline), "CLI replication=%d", st)
				time.Sleep(50 * time.Millisecond)
			}
		}
		st, _, _ = req(cl, base, "PUT", "/b/k", "new-object", etag)
		require(st == 200 || st == 201, "conditional=%d", st)
		st, _, _ = req(cl, base, "PUT", "/b/k", "stale", etag)
		require(st == 412, "CLI stale=%d", st)
		for _, c := range cmds {
			must(c.Process.Signal(syscall.SIGTERM))
		}
		for i, c := range cmds {
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			select {
			case e := <-done:
				require(e == nil, "CLI exit=%v log=%s", e, logs[i])
			case <-time.After(5 * time.Second):
				panic("CLI shutdown timeout")
			}
		}
		return "three CLI subprocesses joined on container loopback, cross-peer GET exact, stale PUT 412, SIGTERM exits cleanly"
	})
}
