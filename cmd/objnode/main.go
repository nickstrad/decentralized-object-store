package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/noderuntime"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

//go:embed help.txt
var help string

var errUsage = errors.New("objnode: invalid options")

type options struct {
	ID              string        `json:"id"`
	Region          string        `json:"region"`
	Listen          string        `json:"listen"`
	HTTP            string        `json:"http"`
	Data            string        `json:"data"`
	Seeds           string        `json:"seeds"`
	K               int           `json:"k"`
	M               int           `json:"m"`
	Stripe          int           `json:"stripe"`
	MaxObject       int64         `json:"max_object"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`
}

func defaults() options {
	return options{HTTP: ":8080", K: storage.DefaultK, M: storage.DefaultM, Stripe: storage.DefaultStripeSize, MaxObject: storage.DefaultMaxObjectSize, ShutdownTimeout: 20 * time.Second}
}

func parse(args []string, stderr io.Writer) (options, error) {
	o := defaults()
	fs := flag.NewFlagSet("objnode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	config := fs.String("config", "", "")
	fs.StringVar(&o.ID, "id", "", "")
	fs.StringVar(&o.Region, "region", "", "")
	fs.StringVar(&o.Listen, "listen", "", "")
	fs.StringVar(&o.HTTP, "http", o.HTTP, "")
	fs.StringVar(&o.Data, "data", "", "")
	fs.StringVar(&o.Seeds, "seeds", "", "")
	fs.IntVar(&o.K, "k", o.K, "")
	fs.IntVar(&o.M, "m", o.M, "")
	fs.IntVar(&o.Stripe, "stripe", o.Stripe, "")
	fs.Int64Var(&o.MaxObject, "max-object", o.MaxObject, "")
	fs.DurationVar(&o.ShutdownTimeout, "shutdown-timeout", o.ShutdownTimeout, "")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("objnode: unexpected arguments %q: %w", fs.Args(), errUsage)
	}
	if *config != "" {
		b, err := os.ReadFile(*config)
		if err != nil {
			return o, fmt.Errorf("objnode: config %s: %w", *config, err)
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		var fileOpts options
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		fileOpts = o
		if err := dec.Decode(&fileOpts); err != nil {
			return o, fmt.Errorf("objnode: config %s: %v: %w", *config, err, errUsage)
		}
		o = merge(fileOpts, o, set)
	}
	if o.ShutdownTimeout <= 0 {
		return o, fmt.Errorf("objnode: shutdown timeout %v: %w", o.ShutdownTimeout, errUsage)
	}
	return o, nil
}

func merge(file, flags options, set map[string]bool) options {
	out := file
	pick := map[string]func(){
		"id": func() { out.ID = flags.ID }, "region": func() { out.Region = flags.Region }, "listen": func() { out.Listen = flags.Listen },
		"http": func() { out.HTTP = flags.HTTP }, "data": func() { out.Data = flags.Data }, "seeds": func() { out.Seeds = flags.Seeds },
		"k": func() { out.K = flags.K }, "m": func() { out.M = flags.M }, "stripe": func() { out.Stripe = flags.Stripe },
		"max-object": func() { out.MaxObject = flags.MaxObject }, "shutdown-timeout": func() { out.ShutdownTimeout = flags.ShutdownTimeout },
	}
	for name, f := range pick {
		if set[name] {
			f()
		}
	}
	return out
}

func (o options) configs(logf func(string, ...any)) (noderuntime.RuntimeConfig, storage.Limits, error) {
	var seeds []env.NodeID
	for _, s := range strings.Split(o.Seeds, ",") {
		if s = strings.TrimSpace(s); s != "" {
			seeds = append(seeds, env.NodeID(s))
		}
	}
	cfg := noderuntime.RuntimeConfig{
		ID: env.NodeID(o.ID), Region: env.Region(o.Region), Listen: o.Listen, DataDir: o.Data, Logf: logf,
		Node: node.Config{
			Seeds: seeds, Layout: storage.Layout{K: o.K, M: o.M, StripeSize: o.Stripe}, MaxObjectSize: o.MaxObject,
			Log: func(line string) { logf("%s", line) },
		},
	}
	if err := cfg.Validate(); err != nil {
		return cfg, storage.Limits{}, fmt.Errorf("objnode: %w: %w", err, errUsage)
	}
	if o.HTTP == "" {
		return cfg, storage.Limits{}, fmt.Errorf("objnode: no http address: %w", errUsage)
	}
	return cfg, storage.Limits{MaxObjectSize: o.MaxObject}, nil
}

func serve(ctx context.Context, o options, logger *log.Logger, ready func(httpAddr string, rt *noderuntime.Runtime)) error {
	cfg, limits, err := o.configs(logger.Printf)
	if err != nil {
		return err
	}
	rt, err := noderuntime.NewRuntime(cfg)
	if err != nil {
		return err
	}
	if err := rt.Start(ctx); err != nil {
		return err
	}
	srv := storage.NewServer(o.HTTP, rt, limits, node.StatusRows()...)
	if err := srv.Start(ctx); err != nil {
		return errors.Join(err, rt.Close())
	}
	logger.Printf("objnode: node %s in region %q serving http on %s", cfg.ID, cfg.Region, srv.Addr())
	if ready != nil {
		ready(srv.Addr(), rt)
	}
	select {
	case <-ctx.Done():
	case <-rt.Done():
	}
	logger.Printf("objnode: shutting down within %v", o.ShutdownTimeout)
	sctx, cancel := context.WithTimeout(context.Background(), o.ShutdownTimeout)
	defer cancel()
	return errors.Join(rt.Err(), srv.Shutdown(sctx), rt.Shutdown(sctx))
}

func run(ctx context.Context, args []string, stderr io.Writer, ready func(string, *noderuntime.Runtime)) int {
	o, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags|log.Lmicroseconds)
	if err := serve(ctx, o, logger, ready); err != nil {
		logger.Printf("objnode: %v", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stderr, nil)
	stop()
	os.Exit(code)
}
