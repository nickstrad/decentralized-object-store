package metrics

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func good() Batch {
	return Batch{SchemaVersion: 1, NodeID: "node", Region: "local", BootID: "boot", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 2, Snapshots: []Snapshot{{Component: "http", Points: []Point{{Name: "objstore_http_requests_total", Labels: []Label{{"operation", "health"}, {"status_class", "2xx"}}, Counter: 9}}}}}
}
func TestValidate(t *testing.T) {
	if err := Validate(good()); err != nil {
		t.Fatal(err)
	}
	cases := []func(*Batch){
		func(b *Batch) { b.SchemaVersion = 0 }, func(b *Batch) { b.Sequence = 0 }, func(b *Batch) { b.NodeID = "" }, func(b *Batch) { b.Region = strings.Repeat("x", 257) }, func(b *Batch) { b.BootID = "\xff" }, func(b *Batch) { b.TimeUnixNano = 0 },
		func(b *Batch) { b.Snapshots[0].Component = "unknown" }, func(b *Batch) { b.Snapshots = append(b.Snapshots, b.Snapshots[0]) }, func(b *Batch) { b.Snapshots[0].Points[0].Name = "unknown" }, func(b *Batch) { b.Snapshots[0].Points[0].Gauge = 1 },
		func(b *Batch) { b.Snapshots[0].Points[0].Labels = nil }, func(b *Batch) { b.Snapshots[0].Points[0].Labels[0].Value = "secret" }, func(b *Batch) { b.Snapshots[0].Points[0].Labels[1] = b.Snapshots[0].Points[0].Labels[0] }, func(b *Batch) { b.Snapshots[0].Points = append(b.Snapshots[0].Points, b.Snapshots[0].Points[0]) },
	}
	for i, f := range cases {
		b := good()
		f(&b)
		if Validate(b) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
func TestCatalogueAndClone(t *testing.T) {
	ds := Catalogue()
	seen := map[string]bool{}
	for _, d := range ds {
		if seen[d.Name] || d.Help == "" || (d.Kind == Counter) != strings.HasSuffix(d.Name, "_total") {
			t.Fatal(d)
		}
		seen[d.Name] = true
	}
	ds[0].Name = "bad"
	for i := range ds {
		if len(ds[i].LabelNames) > 0 {
			ds[i].LabelNames[0] = "bad"
		}
	}
	b := good()
	c := Clone(b)
	c.Snapshots[0].Points[0].Labels[0].Value = "bad"
	if Validate(b) != nil {
		t.Fatal("clone aliases input")
	}
	// Exercise every catalogue series and finite domain, including zeroes.
	b.Snapshots = nil
	for _, d := range Catalogue() {
		component := strings.SplitN(strings.TrimPrefix(d.Name, "objstore_"), "_", 2)[0]
		if len(d.LabelNames) == 0 {
			if len(labelDomains[d.Name]) != 0 {
				t.Fatalf("unexpected label domains for %s", d.Name)
			}
			b.Snapshots = []Snapshot{{component, []Point{{Name: d.Name}}}}
			if err := Validate(b); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if len(labelDomains[d.Name]) != len(d.LabelNames) {
			t.Fatalf("label domain mismatch for %s", d.Name)
		}
		for i, l := range d.LabelNames {
			if i > 0 && d.LabelNames[i-1] >= l {
				t.Fatalf("unsorted label names for %s: %v", d.Name, d.LabelNames)
			}
			vals := labelDomains[d.Name][l]
			if len(vals) == 0 {
				t.Fatalf("empty label domain for %s.%s", d.Name, l)
			}
			for _, v := range vals {
				p := Point{Name: d.Name}
				for _, ln := range d.LabelNames {
					val := labelDomains[d.Name][ln][0]
					if ln == l {
						val = v
					}
					p.Labels = append(p.Labels, Label{ln, val})
				}
				b.Snapshots = []Snapshot{{component, []Point{p}}}
				if err := Validate(b); err != nil {
					t.Fatalf("%s %s=%s: %v", d.Name, l, v, err)
				}
			}
		}
	}
}

func TestErasureByteOperationDomains(t *testing.T) {
	erasureByteMetrics := []string{
		"objstore_erasure_input_bytes_total",
		"objstore_erasure_output_bytes_total",
	}
	for _, name := range erasureByteMetrics {
		for _, op := range []string{"encode", "reconstruct"} {
			for _, count := range []uint64{0, 4096} {
				b := Batch{
					SchemaVersion: SchemaVersion,
					NodeID:        "node",
					Region:        "local",
					BootID:        "boot",
					Sequence:      1,
					StartUnixNano: 1,
					TimeUnixNano:  2,
					Snapshots: []Snapshot{{
						Component: "erasure",
						Points: []Point{{
							Name:    name,
							Labels:  []Label{{Name: "operation", Value: op}},
							Counter: count,
						}},
					}},
				}
				if err := Validate(b); err != nil {
					t.Errorf("%s operation=%s count=%d rejected: %v", name, op, count, err)
				}
			}
		}
		for _, bad := range []string{"health", "members", "bucket", "object", "tag", "acl", "quota", "migration", "unknown", ""} {
			b := Batch{
				SchemaVersion: SchemaVersion,
				NodeID:        "node",
				Region:        "local",
				BootID:        "boot",
				Sequence:      1,
				StartUnixNano: 1,
				TimeUnixNano:  2,
				Snapshots: []Snapshot{{
					Component: "erasure",
					Points: []Point{{
						Name:    name,
						Labels:  []Label{{Name: "operation", Value: bad}},
						Counter: 1,
					}},
				}},
			}
			if err := Validate(b); err == nil {
				t.Errorf("%s accepted invalid operation=%q", name, bad)
			}
		}
	}
}
func TestCoreImportBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json", "../metrics", "../env", "../sim", "../gossip", "../storage", "../erasure", "../merkle", "../occ", "../saga", "../node")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p struct {
			ImportPath string
			Standard   bool
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.ImportPath, "/telemetry") || (!p.Standard && !strings.HasPrefix(p.ImportPath, "github.com/nickstrad/object-store/")) {
			t.Fatalf("core reaches framework: %s", p.ImportPath)
		}
	}
}

func TestMetricsUsesOnlyStandardLibrary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "github.com/nickstrad/object-store/internal/metrics" {
		t.Fatalf("metrics has non-stdlib dependencies: %s", out)
	}
}
