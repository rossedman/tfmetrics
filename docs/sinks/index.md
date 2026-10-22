# Sinks

A sink is where `aggregate` sends its results. Every sink is generated from the same facts files, so you can turn on several at once and add another later without scanning anything again.

| Sink | Use it for | Writes | Tutorial |
| --- | --- | --- | --- |
| `openmetrics` | Prometheus dashboards and alerts | `metrics.prom`, or a push to a Pushgateway | [Prometheus](openmetrics.md) |
| `otlp` | Datadog, Honeycomb, Grafana Cloud and other OpenTelemetry backends | Gauges over OTLP gRPC | [OpenTelemetry](otlp.md) |
| `parquet` | History, trends and SQL in a warehouse | `modules.parquet`, `module_calls.parquet` | [Parquet](parquet.md) |
| `csv` | Spreadsheets and quick looks | `modules.csv`, `module_calls.csv` | [CSV](csv.md) |
| `json` | Archiving a run, your own tooling | `metrics.json` and a copy of every facts file | [JSON](json.md) |
| `markdown` | PR comments and job summaries | One report per repo | [Markdown](markdown.md) |
| `sarif` | Code scanning alerts and PR annotations | One SARIF log per repo | [SARIF](sarif.md) |

## Choose sinks on the command line

`--sink` names the sinks for one run. Repeat it for more than one:

```console
$ tfmetrics aggregate facts/ --offline --sink openmetrics --sink parquet --out metrics/
aggregated 2 repos (2 with Terraform) into 45 series
```

File sinks write into `--out` (default `metrics/`) unless their `path` option says otherwise. With no `--sink` and no `sinks` in the config, `aggregate` writes the `json` sink.

`scan` and `org` take `--sink` too, to scan and aggregate in one step. `scan` never looks up module versions; `org` does, unless you run `aggregate` separately with `--offline`.

## Configure sinks in tfmetrics.yaml

For anything you run more than once, list the sinks under [`sinks`](../configuration.md#sinks). Each item is a one-key map from the sink's name to its options:

```yaml title="tfmetrics.yaml"
sinks:
  - openmetrics:
      path: out/prom/metrics.prom
  - parquet:
      path: out/tables
  - markdown:
      path: out/report.md
```

`aggregate` with no `--sink` then writes all three:

```console
$ tfmetrics aggregate facts/ --offline
aggregated 2 repos (2 with Terraform) into 45 series
$ find out -type f | sort
out/prom/metrics.prom
out/report.md
out/tables/module_calls.parquet
out/tables/modules.parquet
```

`--sink` narrows the run to the sinks you name, and each one still takes its options from the matching entry in the config:

```console
$ tfmetrics aggregate facts/ --offline --sink parquet
aggregated 2 repos (2 with Terraform) into 45 series
$ find out -type f | sort
out/tables/module_calls.parquet
out/tables/modules.parquet
```

## Things every sink shares

- **Version drift needs the network.** `aggregate` looks up module versions for `tf_module_versions_behind`. `--offline` skips the lookups and leaves that metric out; every other metric is unchanged.
- **Labels are shaped before any sink runs.** [`labels.properties`](../configuration.md#labels) adds custom properties as labels, and `labels.drop` removes high-cardinality labels and sums the series that merge. Every gauge sink sees the same series.
- **Local paths only.** File sinks refuse `s3://` and other object-store URLs. Write locally, then upload with `aws s3 cp`, `gcloud storage cp` or similar; the [Parquet tutorial](parquet.md#3-keep-history-in-object-storage) shows the pattern.
- **Re-aggregate any time.** Facts files are the source of truth. Changing a sink, a label or a weight and running `aggregate` again over saved facts regenerates the output without a scan.
