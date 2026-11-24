// Command objnode runs one object-store and metadata-sync node over real
// UDP/TCP sockets, a file-backed disk, and an unauthenticated HTTP API.
//
// Options may be passed via flags or a JSON config file (flags take precedence).
// On SIGINT, SIGTERM, or a recovered panic on the node loop, objnode drains
// HTTP and runtime shutdown within the configured shutdown timeout and exits
// 0 on clean shutdown, 1 on runtime failure, or 2 on invalid options.
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
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/noderuntime"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/telemetry"
)

//go:embed help.txt
var help string

var errUsage = errors.New("objnode: invalid options")

type options struct {
	Observability   telemetry.Config `json:"observability"`
	ID              string           `json:"id"`
	Region          string           `json:"region"`
	Listen          string           `json:"listen"`
	HTTP            string           `json:"http"`
	Data            string           `json:"data"`
	Seeds           string           `json:"seeds"`
	K               int              `json:"k"`
	M               int              `json:"m"`
	Stripe          int              `json:"stripe"`
	MaxObject       int64            `json:"max_object"`
	ShutdownTimeout time.Duration    `json:"shutdown_timeout"`
}

func defaults() options {
	return options{HTTP: ":8080", K: storage.DefaultK, M: storage.DefaultM, Stripe: storage.DefaultStripeSize, MaxObject: httpapi.DefaultMaxObjectSize, ShutdownTimeout: 20 * time.Second}
}

// parse reads CLI flags and an optional JSON config file (rejecting unknown
// fields) and returns the merged options with CLI flags overriding file values.
func parse(args []string, stderr io.Writer) (options, error) {
	o := defaults()
	fs := flag.NewFlagSet("objnode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	config := fs.String("config", "", "")
	fs.BoolVar(&o.Observability.Enabled, "observability", false, "")
	fs.StringVar(&o.Observability.Adapter, "observability-adapter", "", "")
	fs.StringVar(&o.Observability.Interval, "observability-interval", "", "")
	fs.StringVar(&o.Observability.Timeout, "observability-timeout", "", "")
	fs.Func("observability-settings", "", func(value string) error { o.Observability.Settings = json.RawMessage(value); return nil })
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

// merge overlays explicitly provided CLI flags onto options decoded from a
// config file.
func merge(file, flags options, set map[string]bool) options {
	out := file
	pick := map[string]func(){
		"observability":          func() { out.Observability.Enabled = flags.Observability.Enabled },
		"observability-adapter":  func() { out.Observability.Adapter = flags.Observability.Adapter },
		"observability-interval": func() { out.Observability.Interval = flags.Observability.Interval },
		"observability-timeout":  func() { out.Observability.Timeout = flags.Observability.Timeout },
		"observability-settings": func() { out.Observability.Settings = flags.Observability.Settings },
		"id":                     func() { out.ID = flags.ID }, "region": func() { out.Region = flags.Region }, "listen": func() { out.Listen = flags.Listen },
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

// configs validates o and builds the noderuntime and HTTP server configs.
func (o options) configs(logf func(string, ...any)) (noderuntime.RuntimeConfig, httpapi.Limits, error) {
	check := o.Observability
	check.Enabled = false
	if _, err := telemetry.Open(context.Background(), check, telemetry.Identity{}); err != nil {
		return noderuntime.RuntimeConfig{}, httpapi.Limits{}, errors.Join(errUsage, err)
	}
	var seeds []env.NodeID
	for _, s := range strings.Split(o.Seeds, ",") {
		if s = strings.TrimSpace(s); s != "" {
			seeds = append(seeds, env.NodeID(s))
		}
	}
	cfg := noderuntime.RuntimeConfig{
		MetricsEnabled: o.Observability.Enabled,
		ID:             env.NodeID(o.ID), Region: env.Region(o.Region), Listen: o.Listen, DataDir: o.Data, Logf: logf,
		Node: node.Config{
			Seeds: seeds, Layout: storage.Layout{K: o.K, M: o.M, StripeSize: o.Stripe}, MaxObjectSize: o.MaxObject,
			Log: func(line string) { logf("%s", line) },
		},
	}
	if err := cfg.Validate(); err != nil {
		return cfg, httpapi.Limits{}, fmt.Errorf("objnode: %w: %w", err, errUsage)
	}
	if o.HTTP == "" {
		return cfg, httpapi.Limits{}, fmt.Errorf("objnode: no http address: %w", errUsage)
	}
	return cfg, httpapi.Limits{MaxObjectSize: o.MaxObject, MetricsEnabled: o.Observability.Enabled}, nil
}

// serve starts the node runtime and HTTP server, waits for ctx cancellation or
// an unexpected runtime stop, and runs bounded shutdown of both.
func serve(ctx context.Context, o options, logger *log.Logger, ready func(httpAddr string, rt *noderuntime.Runtime)) error {
	cfg, limits, err := o.configs(logger.Printf)
	if err != nil {
		return err
	}
	rt, err := noderuntime.NewRuntime(cfg)
	if err != nil {
		return err
	}
	id := telemetry.NewIdentity(o.ID, o.Region)
	adapter, err := telemetry.Open(ctx, o.Observability, id)
	if err != nil {
		if errors.Is(err, telemetry.ErrConfig) {
			err = errors.Join(errUsage, err)
		}
		return errors.Join(err, rt.Close())
	}
	closeAdapter := func() error {
		if adapter == nil {
			return nil
		}
		sctx, cancel := context.WithTimeout(context.Background(), o.ShutdownTimeout)
		defer cancel()
		return adapter.Shutdown(sctx)
	}
	if err := rt.Start(ctx); err != nil {
		return errors.Join(err, rt.Close(), closeAdapter())
	}
	srv := httpapi.NewServer(o.HTTP, rt, limits, node.StatusRows()...)
	if err := srv.Start(ctx); err != nil {
		return errors.Join(err, rt.Close(), closeAdapter())
	}
	var runner *telemetry.Runner
	var runnerDone chan struct{}
	runnerCtx, stopRunner := context.WithCancel(ctx)
	defer stopRunner()
	if adapter != nil {
		runner, err = telemetry.NewRunner(o.Observability, id, rt.MetricsCollector(srv), adapter, logger.Printf)
		if err != nil {
			return errors.Join(errUsage, err, srv.Shutdown(ctx), rt.Close(), closeAdapter())
		}
		runnerDone = make(chan struct{})
		go func() { defer close(runnerDone); _ = runner.Run(runnerCtx) }()
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
	stopRunner()
	if runnerDone != nil {
		<-runnerDone
	}
	httpErr := srv.Shutdown(sctx)
	runtimeErr := rt.Shutdown(sctx)
	var adapterErr error
	if runner != nil {
		adapterErr = runner.Shutdown(sctx)
	}
	return errors.Join(rt.Err(), httpErr, runtimeErr, adapterErr)
}

// run executes the CLI lifecycle and maps outcomes to process exit codes:
// 0 for help or clean shutdown, 2 for usage/config errors, and 1 for runtime
// errors.
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

var ignoredAtExit = []string{"os/signal.signal_recv", "os/signal.loop", "runtime.ensureSigM"}

func leftGoroutines() []string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var out []string
	for i, block := range strings.Split(string(buf), "\n\n") {
		if i == 0 || strings.TrimSpace(block) == "" {
			continue
		}
		benign := false
		for _, p := range ignoredAtExit {
			benign = benign || strings.Contains(block, p)
		}
		if !benign {
			out = append(out, block)
		}
	}
	return out
}

func leakReport(w io.Writer, settle time.Duration) int {
	deadline := time.Now().Add(settle)
	left := leftGoroutines()
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		left = leftGoroutines()
	}
	fmt.Fprintf(w, "objnode: goroutines left at exit: %d\n", len(left))
	for _, g := range left {
		fmt.Fprintf(w, "%s\n\n", g)
	}
	return len(left)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stderr, nil)
	stop()
	leakReport(os.Stderr, 2*time.Second)
	os.Exit(code)
}
