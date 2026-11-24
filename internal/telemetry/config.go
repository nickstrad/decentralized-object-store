// Package telemetry connects runtime collectors to bounded metric sinks.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/metrics"
	"io"
	"time"
)

var (
	ErrConfig  = errors.New("invalid observability config")
	ErrClosed  = errors.New("telemetry closed")
	ErrPartial = errors.New("partial telemetry export")
)

type Config struct {
	Enabled  bool            `json:"enabled"`
	Adapter  string          `json:"adapter"`
	Interval string          `json:"interval"`
	Timeout  string          `json:"timeout"`
	Settings json.RawMessage `json:"settings"`
}

func (c *Config) UnmarshalJSON(b []byte) error {
	type plain Config
	var v plain
	if err := decodeStrict(b, &v); err != nil {
		return fmt.Errorf("%w: %v", ErrConfig, err)
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("%w: expected object", ErrConfig)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return fmt.Errorf("%w: expected object", ErrConfig)
	}
	for key, value := range fields {
		if key != "settings" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: null common field %s", ErrConfig, key)
		}
	}
	*c = Config(v)
	return nil
}
func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

type Identity struct {
	NodeID, Region, BootID string
	StartUnixNano          int64
}

func NewIdentity(nodeID, region string) Identity {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return Identity{nodeID, region, hex.EncodeToString(b[:]), time.Now().UnixNano()}
}

type Collector interface {
	Collect(context.Context) (metrics.Batch, error)
}
type Adapter interface {
	Publish(context.Context, metrics.Batch) error
	Shutdown(context.Context) error
}
type Factory func(context.Context, Identity, json.RawMessage) (Adapter, error)
type normalized struct {
	name              string
	interval, timeout time.Duration
	settings          json.RawMessage
}

func normalize(c Config) (normalized, error) {
	n := normalized{name: c.Adapter, interval: 10 * time.Second, timeout: 2 * time.Second, settings: c.Settings}
	if n.name == "" {
		n.name = "otel"
	}
	if _, ok := factories[n.name]; !ok {
		return n, fmt.Errorf("%w: unknown adapter %q", ErrConfig, n.name)
	}
	var err error
	if c.Interval != "" {
		n.interval, err = time.ParseDuration(c.Interval)
		if err != nil {
			return n, fmt.Errorf("%w: interval", ErrConfig)
		}
	}
	if c.Timeout != "" {
		n.timeout, err = time.ParseDuration(c.Timeout)
		if err != nil {
			return n, fmt.Errorf("%w: timeout", ErrConfig)
		}
	}
	if n.interval < 100*time.Millisecond || n.interval > time.Hour || n.timeout <= 0 || n.timeout >= n.interval {
		return n, fmt.Errorf("%w: duration bounds", ErrConfig)
	}
	n.settings = bytes.TrimSpace(n.settings)
	if len(n.settings) == 0 || bytes.Equal(n.settings, []byte("null")) {
		n.settings = json.RawMessage(`{}`)
	}
	var o map[string]json.RawMessage
	if n.settings[0] != '{' || json.Unmarshal(n.settings, &o) != nil {
		return n, fmt.Errorf("%w: settings must be an object", ErrConfig)
	}
	if c.Enabled {
		if validate := settingValidators[n.name]; validate != nil {
			if err := validate(n.interval, n.settings); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}
func Open(ctx context.Context, c Config, id Identity) (Adapter, error) {
	n, err := normalize(c)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateIdentity(id); err != nil {
		return nil, err
	}
	return factories[n.name](ctx, id, n.settings)
}
func validateIdentity(id Identity) error {
	b := metrics.Batch{SchemaVersion: metrics.SchemaVersion, NodeID: id.NodeID, Region: id.Region, BootID: id.BootID, Sequence: 1, StartUnixNano: id.StartUnixNano, TimeUnixNano: id.StartUnixNano}
	if err := metrics.Validate(b); err != nil {
		return fmt.Errorf("%w: identity: %v", ErrConfig, err)
	}
	return nil
}
