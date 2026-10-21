# OpenTelemetry

Send tfmetrics gauges over OTLP to an OpenTelemetry Collector, and from there to Datadog, Honeycomb, Grafana Cloud or any backend the Collector supports.

The `otlp` sink exports every series in the [metrics catalog](../reference/metrics.md) as a gauge over gRPC, with the resource attribute `service.name="tfmetrics"` and the same attributes as the Prometheus labels: `repo`, `owner`, `org` and the rest.

## 1. Run a Collector locally

Start with a Collector that only prints what it receives, so you can see the data before wiring a backend. Save this as `otelcol.yaml`:

```yaml title="otelcol.yaml"
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:4317

exporters:
  debug:
    verbosity: detailed

service:
  pipelines:
    metrics:
      receivers: [otlp]
      exporters: [debug]
```

Run it with the [Collector contrib distribution](https://github.com/open-telemetry/opentelemetry-collector-releases/releases):

```console
$ otelcol-contrib validate --config otelcol.yaml
$ otelcol-contrib --config otelcol.yaml
```

## 2. Point tfmetrics at it

```yaml title="tfmetrics.yaml"
sinks:
  - otlp:
      endpoint: 127.0.0.1:4317
      insecure: true
```

`insecure: true` uses plaintext gRPC, which is right for a Collector on the same host or private network. Leave it out for a TLS endpoint.

```console
$ tfmetrics aggregate facts/
aggregated 2 repos (2 with Terraform) into 45 series
```

## 3. Check what arrived

The Collector logs one batch with every series:

```text
info  Metrics  {"otelcol.component.id": "debug", "resource metrics": 1, "metrics": 16, "data points": 45}
```

and, with `verbosity: detailed`, each gauge and its attributes:

```text
Resource attributes:
     -> service.name: Str(tfmetrics)
...
Descriptor:
     -> Name: tf_hygiene_score
     -> Description: Weighted 0-100 hygiene score.
     -> DataType: Gauge
NumberDataPoints #0
Data point attributes:
     -> org: Str(acme)
     -> owner: Str(acme/networking)
     -> repo: Str(acme/tf-network)
Value: 86.000000
```

## 4. Export to your backend

Swap the `debug` exporter for your vendor's. The Collector reads API keys from the environment with `${env:NAME}`, which keeps secrets out of both config files; `tfmetrics.yaml` does no environment expansion, so the Collector is the right place for them.

```yaml title="otelcol.yaml"
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317

processors:
  batch: {}

exporters:
  datadog:
    api:
      key: ${env:DD_API_KEY}
      site: datadoghq.com
  otlphttp/honeycomb:
    endpoint: https://api.honeycomb.io
    headers:
      x-honeycomb-team: ${env:HONEYCOMB_API_KEY}
      x-honeycomb-dataset: tfmetrics
  otlphttp/grafana:
    endpoint: ${env:GRAFANA_OTLP_ENDPOINT}
    auth:
      authenticator: basicauth/grafana

extensions:
  basicauth/grafana:
    client_auth:
      username: ${env:GRAFANA_INSTANCE_ID}
      password: ${env:GRAFANA_API_TOKEN}

service:
  extensions: [basicauth/grafana]
  pipelines:
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [datadog, otlphttp/honeycomb, otlphttp/grafana]
```

Keep only the exporters you use; this file passes `otelcol-contrib validate` as written. Each vendor's docs list its endpoints and regions.

## Without a Collector

The sink can talk to a backend's OTLP gRPC endpoint directly, with headers for the API key:

```yaml title="tfmetrics.yaml"
sinks:
  - otlp:
      endpoint: otlp.example.com:443
      headers:
        x-api-key: "<your key>"
```

Header values are literal. Generate this file in CI from a secret rather than committing it, or use a Collector as above.

## Running it in CI

In a nightly job, run the Collector as a [service container](https://docs.github.com/en/actions/use-cases-and-examples/using-containerized-services/about-service-containers) or a sidecar next to `tfmetrics aggregate`, and keep `endpoint: 127.0.0.1:4317` with `insecure: true`. The Collector holds the vendor credentials and retries exports, so the tfmetrics job stays the same whichever backend you use.
