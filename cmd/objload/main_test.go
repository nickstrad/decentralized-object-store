package main

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/objctl/objctltest"
	"github.com/nickstrad/object-store/internal/objload"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpapi/httpapitest"
	"github.com/nickstrad/object-store/internal/storage/httpclient"
)

func noenv(string) string { return "" }

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(nil, noenv, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := objload.Defaults()
	want.Seed = cfg.Seed
	if cfg.Rate != want.Rate || cfg.Workers != want.Workers || cfg.Profile != "simple" || cfg.MinSize != 1024 || cfg.MaxSize != 16384 ||
		strings.Join(cfg.Addrs, ",") != "127.0.0.1:8080" || cfg.Duration != 0 || cfg.Timeout != want.Timeout {
		t.Errorf("defaults %+v", cfg)
	}
	other, _ := parse(nil, noenv, &bytes.Buffer{})
	if other.Seed == cfg.Seed {
		t.Errorf("two unseeded runs drew seed %d", cfg.Seed)
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parse([]string{"-addr", "a:1, b:2", "-rate", "7.5", "-duration", "3s", "-workers", "2", "-profile", "mixed",
		"-keys", "9", "-buckets", "3", "-size", "10-20", "-seed", "0", "-timeout", "1s"}, noenv, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Addrs, "|") != "a:1|b:2" || cfg.Rate != 7.5 || cfg.Duration != 3*time.Second || cfg.Workers != 2 ||
		cfg.Profile != "mixed" || cfg.Keys != 9 || cfg.Buckets != 3 || cfg.MinSize != 10 || cfg.MaxSize != 20 || cfg.Seed != 0 || cfg.Timeout != time.Second {
		t.Errorf("parsed %+v", cfg)
	}
	env, err := parse(nil, func(k string) string { return map[string]string{"OBJLOAD_ADDR": "n:9"}[k] }, &bytes.Buffer{})
	if err != nil || env.Addrs[0] != "n:9" {
		t.Errorf("OBJLOAD_ADDR: %v %v", err, env.Addrs)
	}
}

func TestHelpListsEveryFlag(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parse([]string{"-h"}, noenv, &stderr); err != flag.ErrHelp {
		t.Fatalf("-h: %v", err)
	}
	for _, name := range []string{"addr", "rate", "duration", "workers", "profile", "keys", "buckets", "size", "seed", "timeout"} {
		if !strings.Contains(stderr.String(), "\n"+name+" ") {
			t.Errorf("help lacks %s", name)
		}
	}
}

func connectTo(tr http.RoundTripper) newService {
	return func(addr string) (httpapi.Service, error) {
		return httpclient.New(addr, httpclient.Options{Transport: tr})
	}
}

func TestRunExitCodes(t *testing.T) {
	fake := httpapitest.InProcess(httpapi.NewHandler(objctltest.NewFake(), httpapi.Limits{}))
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"-h"}, 0, ""},
		{[]string{"-rate", "0"}, 2, "rate 0"},
		{[]string{"-size", "big"}, 2, "size"},
		{[]string{"extra"}, 2, "unexpected arguments"},
		{[]string{"-bogus"}, 2, ""},
		{[]string{"-addr", "a:1,http://b:2/path"}, 2, "address"},
		{[]string{"-addr", "a:1,b:2", "-rate", "500", "-duration", "200ms", "-seed", "5", "-profile", "mixed"}, 0, "summary:"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), tc.args, noenv, &stdout, &stderr, connectTo(fake))
		if code != tc.code || !strings.Contains(stdout.String()+stderr.String(), tc.out) {
			t.Errorf("%v: exit %d, want %d; output:\n%s%s", tc.args, code, tc.code, stdout.String(), stderr.String())
		}
	}
}

type refusing struct{}

func (refusing) RoundTrip(*http.Request) (*http.Response, error) { return nil, http.ErrServerClosed }

func TestRunNothingSucceeded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"-rate", "50"}, noenv, &stdout, &stderr, connectTo(refusing{})); code != 1 {
		t.Errorf("exit %d, want 1:\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no request succeeded") {
		t.Errorf("stderr: %s", stderr.String())
	}
}
