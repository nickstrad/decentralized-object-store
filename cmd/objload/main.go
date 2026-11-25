// Command objload puts a constant, randomized load on object-store nodes
// through the HTTP API, for watching metrics change in real time. The
// generator lives in internal/objload; this package parses flags, builds one
// httpclient per address, and stops on SIGINT or SIGTERM. Run "objload -h"
// for the flags.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nickstrad/object-store/internal/objload"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpclient"
)

//go:embed help.txt
var help string

type newService func(addr string) (httpapi.Service, error)

func httpService(addr string) (httpapi.Service, error) {
	return httpclient.New(addr, httpclient.Options{})
}

// parse reads the flags into a validated objload.Config. A seed left unset is
// drawn at random.
func parse(args []string, getenv func(string) string, stderr io.Writer) (objload.Config, error) {
	cfg := objload.Defaults()
	fs := flag.NewFlagSet("objload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	addr := strings.Join(cfg.Addrs, ",")
	if v := getenv("OBJLOAD_ADDR"); v != "" {
		addr = v
	}
	fs.StringVar(&addr, "addr", addr, "")
	fs.Float64Var(&cfg.Rate, "rate", cfg.Rate, "")
	fs.DurationVar(&cfg.Duration, "duration", cfg.Duration, "")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "")
	fs.StringVar(&cfg.Profile, "profile", cfg.Profile, "")
	fs.IntVar(&cfg.Keys, "keys", cfg.Keys, "")
	fs.IntVar(&cfg.Buckets, "buckets", cfg.Buckets, "")
	size := fs.String("size", "1K-16K", "")
	fs.Uint64Var(&cfg.Seed, "seed", 0, "")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() != 0 {
		return cfg, fmt.Errorf("objload: unexpected arguments %q: %w", fs.Args(), objload.ErrConfig)
	}
	seeded := false
	fs.Visit(func(f *flag.Flag) { seeded = seeded || f.Name == "seed" })
	if !seeded {
		cfg.Seed = rand.Uint64()
	}
	cfg.Addrs = nil
	for _, a := range strings.Split(addr, ",") {
		cfg.Addrs = append(cfg.Addrs, strings.TrimSpace(a))
	}
	var err error
	if cfg.MinSize, cfg.MaxSize, err = objload.ParseSize(*size); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, connect newService) int {
	cfg, err := parse(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "objload: %v\n", err)
		return 2
	}
	svcs := make([]httpapi.Service, len(cfg.Addrs))
	for i, a := range cfg.Addrs {
		if svcs[i], err = connect(a); err != nil {
			fmt.Fprintf(stderr, "objload: %v\n", err)
			return 2
		}
	}
	s, err := objload.Run(ctx, cfg, svcs, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "objload: %v\n", err)
		return 2
	}
	s.Write(stdout)
	if !s.Succeeded() {
		fmt.Fprintln(stderr, "objload: no request succeeded")
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, httpService)
	stop()
	os.Exit(code)
}
