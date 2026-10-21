package sink

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

// OTLP sends the metrics as OpenTelemetry gauges over gRPC.
type OTLP struct {
	Endpoint string
	Insecure bool
	Headers  map[string]string
}

// Emit exports every series once and flushes.
func (o *OTLP) Emit(ctx context.Context, _ []facts.ScanResult, m metrics.Metrics) error {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(o.Endpoint)}
	if o.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(o.Headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(o.Headers))
	}
	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return err
	}
	return Export(ctx, exp, m)
}

// Export records every series as a gauge on a meter provider backed by exp,
// then flushes and shuts it down.
func Export(ctx context.Context, exp sdkmetric.Exporter, m metrics.Metrics) error {
	reader := sdkmetric.NewManualReader()
	res := resource.NewSchemaless(attribute.String("service.name", "tfmetrics"))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	meter := mp.Meter("github.com/rossedman/tfmetrics")
	names, groups := byName(m.Series)
	for _, n := range names {
		g, err := meter.Float64Gauge(n, metric.WithDescription(metrics.Help[n]))
		if err != nil {
			return err
		}
		for _, s := range groups[n] {
			attrs := make([]attribute.KeyValue, 0, len(s.Labels))
			for k, v := range s.Labels {
				attrs = append(attrs, attribute.String(k, v))
			}
			g.Record(ctx, s.Value, metric.WithAttributes(attrs...))
		}
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		return err
	}
	if err := exp.Export(ctx, &rm); err != nil {
		_ = exp.Shutdown(ctx)
		return err
	}
	if err := exp.Shutdown(ctx); err != nil {
		return err
	}
	return mp.Shutdown(ctx)
}
