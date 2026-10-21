package sink

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/rossedman/tfmetrics/internal/metrics"
)

// fakeExporter is an in-memory sdkmetric.Exporter.
type fakeExporter struct {
	mu          sync.Mutex
	exported    []*metricdata.ResourceMetrics
	shutdowns   int
	exportErr   error
	shutdownErr error
}

func (f *fakeExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (f *fakeExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (f *fakeExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exportErr != nil {
		return f.exportErr
	}
	f.exported = append(f.exported, rm)
	return nil
}

func (f *fakeExporter) ForceFlush(context.Context) error { return nil }

func (f *fakeExporter) Shutdown(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	return f.shutdownErr
}

func gauges(rm *metricdata.ResourceMetrics) map[string][]metricdata.DataPoint[float64] {
	out := map[string][]metricdata.DataPoint[float64]{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[float64]); ok {
				out[m.Name] = append(out[m.Name], g.DataPoints...)
			}
		}
	}
	return out
}

func TestExport(t *testing.T) {
	t.Parallel()
	m := fixtureMetrics(fixtureResults())
	exp := &fakeExporter{}
	if err := Export(context.Background(), exp, m); err != nil {
		t.Fatal(err)
	}
	if len(exp.exported) != 1 || exp.shutdowns != 1 {
		t.Fatalf("exports=%d shutdowns=%d", len(exp.exported), exp.shutdowns)
	}
	rm := exp.exported[0]
	if v, ok := rm.Resource.Set().Value("service.name"); !ok || v.AsString() != "tfmetrics" {
		t.Errorf("service.name = %v, %v", v, ok)
	}
	got := gauges(rm)
	byName := map[string]int{}
	for _, s := range m.Series {
		byName[s.Name]++
	}
	for name, n := range byName {
		if len(got[name]) != n {
			t.Errorf("%s: %d points, want %d", name, len(got[name]), n)
		}
	}
	// Every series arrives with its value and attributes.
	for _, s := range m.Series {
		found := false
		for _, dp := range got[s.Name] {
			ok := dp.Value == s.Value && dp.Attributes.Len() == len(s.Labels)
			for k, v := range s.Labels {
				if a, has := dp.Attributes.Value(attribute.Key(k)); !has || a.AsString() != v {
					ok = false
				}
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("series %s%v=%v missing", s.Name, s.Labels, s.Value)
		}
	}
	for _, sm := range rm.ScopeMetrics {
		for _, mm := range sm.Metrics {
			if mm.Description != metrics.Help[mm.Name] {
				t.Errorf("%s description = %q", mm.Name, mm.Description)
			}
		}
	}
}

func TestExportEmpty(t *testing.T) {
	t.Parallel()
	exp := &fakeExporter{}
	if err := Export(context.Background(), exp, metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	if len(exp.exported) != 1 || len(gauges(exp.exported[0])) != 0 {
		t.Errorf("exported = %+v", exp.exported)
	}
}

func TestExportErrors(t *testing.T) {
	t.Parallel()
	m := fixtureMetrics(fixtureResults())
	boom := errors.New("boom")
	exp := &fakeExporter{exportErr: boom}
	if err := Export(context.Background(), exp, m); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if exp.shutdowns != 1 {
		t.Errorf("exporter should be shut down after a failed export; shutdowns=%d", exp.shutdowns)
	}
	exp = &fakeExporter{shutdownErr: boom}
	if err := Export(context.Background(), exp, m); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// An invalid instrument name is rejected by the SDK.
	bad := metrics.Metrics{Series: []metrics.Series{{Name: "9 bad name!", Labels: map[string]string{}, Value: 1}}}
	if err := Export(context.Background(), &fakeExporter{}, bad); err == nil {
		t.Error("want error for invalid instrument name")
	}
	// Cancelled context fails Collect.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Export(ctx, &fakeExporter{}, m); err == nil {
		t.Error("want error for cancelled context")
	}
}

// fakeCollector is a fake OTLP/gRPC metrics collector.
type fakeCollector struct {
	colmetrics.UnimplementedMetricsServiceServer
	mu   sync.Mutex
	reqs []*colmetrics.ExportMetricsServiceRequest
	md   []metadata.MD
}

func (c *fakeCollector) Export(ctx context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	c.md = append(c.md, md)
	return &colmetrics.ExportMetricsServiceResponse{}, nil
}

func startCollector(t *testing.T) (*fakeCollector, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeCollector{}
	srv := grpc.NewServer()
	colmetrics.RegisterMetricsServiceServer(srv, c)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return c, lis.Addr().String()
}

func kvMap(kvs []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		out[kv.Key] = kv.Value.GetStringValue()
	}
	return out
}

func TestOTLPEmitToFakeCollector(t *testing.T) {
	t.Parallel()
	c, addr := startCollector(t)
	results := fixtureResults()
	m := fixtureMetrics(results)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o := &OTLP{Endpoint: addr, Insecure: true, Headers: map[string]string{"x-api-key": "sekret"}}
	if err := o.Emit(ctx, results, m); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		t.Fatal("collector received nothing")
	}
	if got := c.md[0].Get("x-api-key"); len(got) != 1 || got[0] != "sekret" {
		t.Errorf("x-api-key metadata = %v", got)
	}
	points := map[string][]map[string]string{}
	values := map[string][]float64{}
	for _, req := range c.reqs {
		for _, rm := range req.ResourceMetrics {
			if kvMap(rm.Resource.Attributes)["service.name"] != "tfmetrics" {
				t.Errorf("resource attrs = %v", rm.Resource.Attributes)
			}
			for _, sm := range rm.ScopeMetrics {
				for _, mm := range sm.Metrics {
					g := mm.GetGauge()
					if g == nil {
						t.Errorf("%s is not a gauge", mm.Name)
						continue
					}
					if mm.Description != metrics.Help[mm.Name] {
						t.Errorf("%s description = %q", mm.Name, mm.Description)
					}
					for _, dp := range g.DataPoints {
						points[mm.Name] = append(points[mm.Name], kvMap(dp.Attributes))
						values[mm.Name] = append(values[mm.Name], dp.GetAsDouble())
					}
				}
			}
		}
	}
	want := map[string]int{}
	for _, s := range m.Series {
		want[s.Name]++
	}
	for name, n := range want {
		if len(points[name]) != n {
			t.Errorf("%s: %d points, want %d", name, len(points[name]), n)
		}
	}
	for _, s := range m.Series {
		found := false
		for i, attrs := range points[s.Name] {
			ok := values[s.Name][i] == s.Value && len(attrs) == len(s.Labels)
			for k, v := range s.Labels {
				if attrs[k] != v {
					ok = false
				}
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("series %s%v=%v did not arrive", s.Name, s.Labels, s.Value)
		}
	}
}

func TestOTLPEmitUnreachable(t *testing.T) {
	t.Parallel()
	// Grab a free port, then close it so nothing listens.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = (&OTLP{Endpoint: addr, Insecure: true}).Emit(ctx, nil, fixtureMetrics(fixtureResults()))
	if err == nil {
		t.Fatal("want error exporting to a closed port")
	}
}
