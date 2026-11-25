// Package objload generates a constant, randomized load against the object
// store's HTTP API for watching metrics change. It holds the pure parts of the
// generator (configuration, the weighted operation mix, request planning from
// a seed, open-loop pacing and statistics) and a runner that drives any
// httpapi.Service; cmd/objload wires it to real nodes through httpclient.
package objload

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// ErrConfig marks an invalid Config or flag value.
var ErrConfig = errors.New("objload: invalid configuration")

// Config is one load run. Zero values are not defaults: start from Defaults.
type Config struct {
	// Addrs are the node addresses; request i goes to Addrs[i%len(Addrs)].
	Addrs []string
	// Rate is the target in requests per second, independent of latency.
	Rate float64
	// Duration bounds the run; 0 runs until the context is cancelled.
	Duration time.Duration
	// Workers bounds the requests in flight.
	Workers int
	// Profile names the operation mix (see Profiles).
	Profile string
	// Keys is the number of distinct object keys per bucket.
	Keys int
	// Buckets is the number of buckets, named <BucketPrefix>-<n>.
	Buckets int
	// BucketPrefix names the buckets.
	BucketPrefix string
	// MinSize and MaxSize bound the PUT payload size in bytes.
	MinSize, MaxSize int
	// Seed makes the planned request sequence reproducible.
	Seed uint64
	// Timeout bounds each request.
	Timeout time.Duration
	// Report is the period of the status line; 0 disables it.
	Report time.Duration
}

// Defaults returns a simple load: 20 requests per second over 8 workers,
// 100 keys in each of 2 buckets, payloads of 1 KiB to 16 KiB.
func Defaults() Config {
	return Config{
		Addrs: []string{"127.0.0.1:8080"}, Rate: 20, Workers: 8, Profile: "simple",
		Keys: 100, Buckets: 2, BucketPrefix: "objload", MinSize: 1 << 10, MaxSize: 16 << 10,
		Timeout: 5 * time.Second, Report: time.Second,
	}
}

// Validate reports every invalid field of c, wrapped in ErrConfig.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if len(c.Addrs) == 0 {
		bad("no address")
	}
	for _, a := range c.Addrs {
		if strings.TrimSpace(a) == "" {
			bad("empty address in %q", strings.Join(c.Addrs, ","))
		}
	}
	if !(c.Rate > 0 && c.Rate <= 100000) {
		bad("rate %v: want a number in (0, 100000]", c.Rate)
	}
	if c.Duration < 0 {
		bad("duration %v is negative", c.Duration)
	}
	if c.Workers < 1 || c.Workers > 4096 {
		bad("workers %d: want 1 to 4096", c.Workers)
	}
	if _, ok := Profiles[c.Profile]; !ok {
		bad("profile %q: want one of %s", c.Profile, strings.Join(ProfileNames(), ", "))
	}
	if c.Keys < 1 || c.Keys > 1000000 {
		bad("keys %d: want 1 to 1000000", c.Keys)
	}
	if c.Buckets < 1 || c.Buckets > 100 {
		bad("buckets %d: want 1 to 100", c.Buckets)
	} else if err := storage.ValidBucket(BucketName(c.BucketPrefix, c.Buckets-1)); err != nil {
		bad("bucket prefix %q: %v", c.BucketPrefix, err)
	}
	if c.MinSize < 0 || c.MaxSize < c.MinSize || c.MaxSize > httpapi.DefaultMaxObjectSize {
		bad("size %d-%d: want 0 <= min <= max <= %d", c.MinSize, c.MaxSize, httpapi.DefaultMaxObjectSize)
	}
	if c.Timeout <= 0 {
		bad("timeout %v: want a positive duration", c.Timeout)
	}
	if c.Report < 0 {
		bad("report period %v is negative", c.Report)
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrConfig, errors.Join(errs...))
}

// BucketName returns the name of bucket n.
func BucketName(prefix string, n int) string { return prefix + "-" + strconv.Itoa(n) }

// ProfileNames returns the profile names in sorted order.
func ProfileNames() []string {
	var names []string
	for n := range Profiles {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ParseSize parses a payload size range: "N" or "MIN-MAX", each a whole
// number of bytes with an optional K or M suffix (KiB, MiB).
func ParseSize(s string) (lo, hi int, err error) {
	a, b, ok := strings.Cut(s, "-")
	if lo, err = parseBytes(a); err != nil {
		return 0, 0, err
	}
	hi = lo
	if ok {
		if hi, err = parseBytes(b); err != nil {
			return 0, 0, err
		}
	}
	if hi < lo {
		return 0, 0, fmt.Errorf("%w: size %q: max below min", ErrConfig, s)
	}
	return lo, hi, nil
}

func parseBytes(s string) (int, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := 1
	for _, u := range []struct {
		suffix string
		mult   int
	}{{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"K", 1 << 10}, {"M", 1 << 20}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSuffix(t, u.suffix), u.mult
			break
		}
	}
	n, err := strconv.Atoi(t)
	if err != nil || n < 0 || n > httpapi.DefaultMaxObjectSize/mult {
		return 0, fmt.Errorf("%w: size %q: want a whole number of bytes, optionally with K or M", ErrConfig, s)
	}
	return n * mult, nil
}
