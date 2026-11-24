package storage

import (
	"errors"
	"github.com/nickstrad/object-store/internal/erasure"
	"github.com/nickstrad/object-store/internal/metrics"
	"strings"
)

type metricState struct{ points map[string]metrics.Point }

func metricKey(name string, labels ...string) string { return name + "|" + strings.Join(labels, "|") }
func newMetricState(enabled bool) *metricState {
	if !enabled {
		return nil
	}
	m := &metricState{points: make(map[string]metrics.Point)}
	m.points[metricKey("storage_shard_operations_total", "put", "ok")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("storage_shard_operations_total", "put", "not_found")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "not_found"}}}
	m.points[metricKey("storage_shard_operations_total", "put", "corrupt")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "corrupt"}}}
	m.points[metricKey("storage_shard_operations_total", "put", "error")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("storage_shard_operations_total", "get", "ok")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("storage_shard_operations_total", "get", "not_found")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "not_found"}}}
	m.points[metricKey("storage_shard_operations_total", "get", "corrupt")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "corrupt"}}}
	m.points[metricKey("storage_shard_operations_total", "get", "error")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("storage_shard_operations_total", "delete", "ok")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("storage_shard_operations_total", "delete", "not_found")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "not_found"}}}
	m.points[metricKey("storage_shard_operations_total", "delete", "corrupt")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "corrupt"}}}
	m.points[metricKey("storage_shard_operations_total", "delete", "error")] = metrics.Point{Name: "objstore_storage_shard_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("storage_shard_bytes_total", "read")] = metrics.Point{Name: "objstore_storage_shard_bytes_total", Labels: []metrics.Label{{Name: "direction", Value: "read"}}}
	m.points[metricKey("storage_shard_bytes_total", "write")] = metrics.Point{Name: "objstore_storage_shard_bytes_total", Labels: []metrics.Label{{Name: "direction", Value: "write"}}}
	m.points[metricKey("storage_rpc_timeouts_total")] = metrics.Point{Name: "objstore_storage_rpc_timeouts_total", Labels: []metrics.Label{}}
	m.points[metricKey("storage_requests_in_flight")] = metrics.Point{Name: "objstore_storage_requests_in_flight", Labels: []metrics.Label{}}
	m.points[metricKey("storage_local_shards")] = metrics.Point{Name: "objstore_storage_local_shards", Labels: []metrics.Label{}}
	m.points[metricKey("storage_local_bytes")] = metrics.Point{Name: "objstore_storage_local_bytes", Labels: []metrics.Label{}}
	return m
}
func (m *metricState) add(name string, n uint64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p, ok := m.points[k]
	if !ok {
		return
	}
	if ^uint64(0)-p.Counter < n {
		p.Counter = ^uint64(0)
	} else {
		p.Counter += n
	}
	m.points[k] = p
}
func (m *metricState) set(name string, n uint64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p, ok := m.points[k]
	if !ok {
		return
	}
	p.Gauge = n
	m.points[k] = p
}
func (m *metricState) snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "storage"}
	if m == nil {
		return s
	}
	for _, k := range []string{"storage_shard_operations_total|put|ok", "storage_shard_operations_total|put|not_found", "storage_shard_operations_total|put|corrupt", "storage_shard_operations_total|put|error", "storage_shard_operations_total|get|ok", "storage_shard_operations_total|get|not_found", "storage_shard_operations_total|get|corrupt", "storage_shard_operations_total|get|error", "storage_shard_operations_total|delete|ok", "storage_shard_operations_total|delete|not_found", "storage_shard_operations_total|delete|corrupt", "storage_shard_operations_total|delete|error", "storage_shard_bytes_total|read", "storage_shard_bytes_total|write", "storage_rpc_timeouts_total|", "storage_requests_in_flight|", "storage_local_shards|", "storage_local_bytes|"} {
		p := m.points[k]
		p.Labels = append([]metrics.Label(nil), p.Labels...)
		s.Points = append(s.Points, p)
	}
	return s
}

func (m *metricState) adjust(name string, delta int64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p := m.points[k]
	if delta < 0 {
		d := uint64(-delta)
		if d > p.Gauge {
			p.Gauge = 0
		} else {
			p.Gauge -= d
		}
	} else if ^uint64(0)-p.Gauge < uint64(delta) {
		p.Gauge = ^uint64(0)
	} else {
		p.Gauge += uint64(delta)
	}
	m.points[k] = p
}

func (s *ChunkStore) enableMetrics(m *metricState) {
	s.metric = m
	if m == nil {
		return
	}
	s.sizes = make(map[string]uint64)
	// Startup inventory is read once. Snapshot never reads the disk.
	if names, err := s.disk.List(blobPrefix); err == nil {
		for _, name := range names {
			if _, err := ParseBlobName(name); err == nil {
				if b, err := s.disk.Read(name); err == nil {
					s.sizes[name] = uint64(len(b))
					s.metric.adjust("storage_local_shards", 1)
					s.metric.adjust("storage_local_bytes", int64(len(b)))
				}
			}
		}
	}
}
func (s *ChunkStore) observedWrite(id ShardID, b []byte) {
	if s.metric == nil {
		return
	}
	s.metric.add("storage_shard_bytes_total", uint64(len(b)), "write")
	name := id.BlobName()
	old, had := s.sizes[name]
	if !had {
		s.metric.adjust("storage_local_shards", 1)
	}
	s.metric.adjust("storage_local_bytes", int64(len(b))-int64(old))
	s.sizes[name] = uint64(len(b))
}
func (s *ChunkStore) observedDelete(id ShardID) {
	if s.metric == nil {
		return
	}
	name := id.BlobName()
	if old, had := s.sizes[name]; had {
		s.metric.adjust("storage_local_shards", -1)
		s.metric.adjust("storage_local_bytes", -int64(old))
		delete(s.sizes, name)
	}
}
func (s *ChunkStore) observedOperation(op string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if errors.Is(err, ErrNotFound) {
			outcome = "not_found"
		} else if errors.Is(err, ErrCorrupt) {
			outcome = "corrupt"
		}
	}
	s.metric.add("storage_shard_operations_total", 1, op, outcome)
}
func (n *Node) Snapshot() metrics.Snapshot        { return n.metric.snapshot() }
func (n *Node) ErasureSnapshot() metrics.Snapshot { return n.codec.(*erasure.MeteredCodec).Snapshot() }
func (n *Node) NewCodec(k, m int) (*erasure.MeteredCodec, error) {
	c, err := erasure.NewMetered(k, m, n.cfg.MetricsEnabled)
	if err == nil {
		c.ShareMetrics(n.codec.(*erasure.MeteredCodec))
	}
	return c, err
}
func (n *Node) ShareMetrics(owner *Node) {
	n.metric = owner.metric
	n.codec.(*erasure.MeteredCodec).ShareMetrics(owner.codec.(*erasure.MeteredCodec))
	n.store.metric = owner.store.metric
	n.store.sizes = owner.store.sizes
}
