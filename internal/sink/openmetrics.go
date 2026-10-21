package sink

import (
	"context"
	"os"
	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/prometheus/common/expfmt"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

// OpenMetrics writes a text exposition file, or pushes to a Pushgateway when
// Pushgateway is set.
type OpenMetrics struct {
	Path        string
	Pushgateway string
	Job         string
}

// collector exposes precomputed series as const gauges.
type collector struct {
	descs  map[string]*prometheus.Desc
	labels map[string][]string
	series []metrics.Series
}

// newCollector builds one Desc per metric, with every label any series uses.
func newCollector(series []metrics.Series) *collector {
	c := &collector{descs: map[string]*prometheus.Desc{}, labels: map[string][]string{}, series: series}
	names, groups := byName(series)
	for _, n := range names {
		ls := labelNames(groups[n])
		c.labels[n] = ls
		c.descs[n] = prometheus.NewDesc(n, metrics.Help[n], ls, nil)
	}
	return c
}

// Describe sends every Desc.
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

// Collect sends every series.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.series {
		ls := c.labels[s.Name]
		vals := make([]string, len(ls))
		for i, l := range ls {
			vals[i] = s.Labels[l]
		}
		ch <- prometheus.MustNewConstMetric(c.descs[s.Name], prometheus.GaugeValue, s.Value, vals...)
	}
}

// Registry returns a Prometheus registry holding the metrics.
func Registry(m metrics.Metrics) (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()
	if err := reg.Register(newCollector(m.Series)); err != nil {
		return nil, err
	}
	return reg, nil
}

// Emit writes or pushes the metrics.
func (o *OpenMetrics) Emit(ctx context.Context, _ []facts.ScanResult, m metrics.Metrics) error {
	reg, err := Registry(m)
	if err != nil {
		return err
	}
	if o.Pushgateway != "" {
		return push.New(o.Pushgateway, o.Job).Gatherer(reg).PushContext(ctx)
	}
	if err := checkLocal(o.Path); err != nil {
		return err
	}
	mfs, err := reg.Gather()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(o.Path)
	if err != nil {
		return err
	}
	enc := expfmt.NewEncoder(f, expfmt.NewFormat(expfmt.TypeOpenMetrics))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			_ = f.Close()
			return err
		}
	}
	if c, ok := enc.(expfmt.Closer); ok {
		if err := c.Close(); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}
