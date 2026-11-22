package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/noderuntime"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"net"
	"time"
)

type holdMigration struct {
	saga.Step
	entered chan struct{}
}

func (s holdMigration) Do(_ saga.Call, _ func([]byte, error)) { close(s.entered) }
func shutdownChecks() {
	check("C23,C22", "loop-panic-closes-sockets", 0, func() string {
		rt, cleanup := realNode()
		defer cleanup()
		addr := rt.Addr()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		e := rt.Inspect(ctx, func(*node.Node) { panic("independent loop check") })
		require(errors.Is(e, env.ErrClosed), "panic inspect=%v", e)
		select {
		case <-rt.Done():
		case <-ctx.Done():
			panic("panic failed to stop runtime")
		}
		require(errors.Is(rt.Err(), noderuntime.ErrPanicked), "err=%v", rt.Err())
		c, e := net.DialTimeout("tcp", addr, time.Second)
		if e == nil {
			c.Close()
			panic("listener survives panic")
		}
		return "Inspect panic terminated loop and listener before Close"
	})
	check("C21,C22", "shutdown-active-migration", 0, func() string {
		// Hold the first migration step so shutdown is provably during a live saga.
		entered := make(chan struct{})
		rt, cleanup := realNodeWithTune(func(cfg *node.Config) {
			cfg.WrapMigration = func(d saga.Definition) saga.Definition {
				d.Steps = append([]saga.Step(nil), d.Steps...)
				d.Steps[0] = holdMigration{d.Steps[0], entered}
				return d
			}
		})
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		must(rt.CreateBucket(ctx, storage.BucketSpec{Name: "b", Region: "r0"}))
		mi, e := rt.StartMigration(ctx, "b", "r1")
		must(e)
		select {
		case <-entered:
		case <-ctx.Done():
			panic("migration never entered step")
		}
		st, e := rt.Migration(ctx, mi.ID)
		must(e)
		require(st.State != "completed" && st.State != "compensated", "migration already terminal")
		addr := rt.Addr()
		must(rt.Shutdown(ctx))
		select {
		case <-rt.Done():
		default:
			panic("shutdown returned before Done")
		}
		c, e := net.DialTimeout("tcp", addr, time.Second)
		if e == nil {
			c.Close()
			panic("listener survives shutdown")
		}
		_, e = rt.Migration(ctx, mi.ID)
		require(errors.Is(e, env.ErrClosed), "post-shutdown migration=%v", e)
		return fmt.Sprintf("shutdown while %s step held; Done closed, listener closed, service refuses calls", st.State)
	})
}
