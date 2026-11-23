package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
	"slices"
	"time"
)

type repeatNet struct {
	env.Transport
	enabled                    *bool
	requests, duplicates, lost *int
}

func (n repeatNet) Request(to env.NodeID, b []byte, d time.Duration, cb func([]byte, error)) {
	if !*n.enabled || len(b) == 0 || b[0] != 2 {
		n.Transport.Request(to, b, d, cb)
		return
	}
	*n.requests++
	seq := *n.requests
	n.Transport.Request(to, b, d, func([]byte, error) {})
	*n.duplicates++
	n.Transport.Request(to, b, d, func(out []byte, e error) {
		if e == nil && seq%7 == 0 {
			*n.lost++
			cb(nil, env.ErrTimeout)
		} else {
			cb(out, e)
		}
	})
}
func duplicateScenario(seed int64) string {
	enabled := true
	requests, duplicates, lost := 0, 0, 0
	c := makeCluster(seed, func(e env.Env) env.Env { return e.WithNet(repeatNet{e.Net, &enabled, &requests, &duplicates, &lost}) })
	c.bucket("", 0)
	want := map[string][]byte{}
	for j := range 9 {
		k := fmt.Sprintf("k%d", j%3)
		b := bytes.Repeat([]byte{byte(j)}, 37+j*271)
		_, e := c.put(j%9, k, b, storage.Condition{})
		if e == nil {
			want[k] = b
		} else {
			require(errors.Is(e, storage.ErrUnavailable) || errors.Is(e, env.ErrTimeout), "unexpected PUT error: %v", e)
		}
		c.settle()
	}
	enabled = false
	c.run(10 * time.Second)
	c.settle()
	var size uint64
	objs, e := c.n(0).ListObjects("b", "", 100)
	must(e)
	for _, o := range objs {
		size += uint64(o.Size)
	}
	require(len(want) > 0, "no acknowledged writes")
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i := range c.ids {
		for _, k := range keys {
			b := want[k]
			_, got, e := c.get(i, k)
			must(e)
			require(bytes.Equal(got, b), "acknowledged bytes changed key=%s node=%d", k, i)
		}
		q, e := c.n(i).Quota("b")
		must(e)
		require(q.UsedBytes == size && q.UsedObjects == uint64(len(objs)), "duplicate quota node=%d got=%+v truth=%d/%d", i, q, size, len(objs))
	}
	require(duplicates > 0 && lost > 0, "no fault applied dup=%d lost=%d", duplicates, lost)
	return fmt.Sprintf("duplicated=%d lost-successful-replies=%d; acknowledged-keys=%d; acknowledged bytes and quota exact after heal", duplicates, lost, len(want))
}
