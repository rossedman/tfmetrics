# Dashboards and trends

Pick where the numbers go. Send gauges to Prometheus or any OpenTelemetry backend for dashboards and alerts, and keep Parquet tables for questions about change over time.

Every sink is generated from the same facts, so you can use several at once. The [sinks overview](../sinks/index.md) shows how to choose and configure them, with a tutorial for each.

| Sink | Best for | Output |
| --- | --- | --- |
| `openmetrics` | Prometheus dashboards and alerts | A text file, or a push to a Pushgateway |
| `otlp` | Grafana Cloud, Datadog, Honeycomb and other OpenTelemetry backends | Gauges over gRPC |
| `parquet` | Trends, ad hoc SQL, a data lake | `modules.parquet` and `module_calls.parquet` |
| `csv` | Spreadsheets and quick looks | `modules.csv` and `module_calls.csv` |
| `json` | Archiving a run, custom tooling | `metrics.json` plus a copy of the facts |
| `markdown` | PR comments and job summaries | One report per repo |

## Prometheus

Push each run to a Pushgateway, or serve the OpenMetrics file, and let Prometheus scrape it. The [Prometheus tutorial](../sinks/openmetrics.md) sets up either path, the scrape config and an alert for a job that stopped running. Once the gauges are in, these queries are a good first dashboard.

### Queries to start from

These are written against the [metrics catalog](../reference/metrics.md); add a `team` label with [`labels.properties`](../configuration.md#labels) to group by it.

```promql
# Repos with Terraform, per org
sum by (org) (tf_repos_total)

# Average hygiene score per owner, lowest first
sort(avg by (owner) (tf_hygiene_score))

# Unpinned remote module calls per owner
sum by (owner) (tf_module_calls_unpinned) > 0

# Root modules with no required_version
sum(tf_required_version{constraint="none"})

# The ten modules furthest behind their latest release
topk(10, tf_module_versions_behind)

# Who still uses an old version of one module
sum by (version, owner) (tf_module_calls{address="app.terraform.io/acme/sqs/aws"})
```

## OpenTelemetry

The `otlp` sink sends the same gauges to an OpenTelemetry Collector, which forwards them to Datadog, Honeycomb, Grafana Cloud or any other backend it supports and holds the vendor credentials. The [OpenTelemetry tutorial](../sinks/otlp.md) runs a Collector locally first, then wires the exporters.

## Trends with Parquet

Gauges answer "what is true now". For "how fast are we pinning modules?" you want rows, not series, so keep the Parquet tables from every run. `parquet` writes two files per run:

- `modules.parquet`: one row per module directory, with `repo`, `owner`, `commit`, `scanned_at`, `path`, `kind`, `lines`, `resources`, `module_calls`, `variables_untyped`, `parse_errors` and the rest of the per-module counts.
- `module_calls.parquet`: one row per module call, with `repo`, `module_path`, `source`, `source_kind`, `address`, `version`, `pinned`, `count` and `for_each`.

Write each night's run to its own partition and upload it. The sinks write local paths only, so copy afterwards:

```sh
day=$(date -u +%F)
tfmetrics aggregate facts/ --sink parquet --out "metrics/dt=$day"
aws s3 cp --recursive "metrics/dt=$day" "s3://acme-data/tfmetrics/dt=$day"
```

Then any engine that reads Parquet can query the whole history. This [DuckDB](https://duckdb.org) query counts unpinned remote calls per day, after two repos pinned `tf-iam` between runs:

```sql
SELECT dt,
       count(*) FILTER (WHERE pinned = 'none') AS unpinned,
       count(*) AS remote_calls
FROM read_parquet('metrics/dt=*/module_calls.parquet', hive_partitioning = true)
WHERE source_kind <> 'local'
GROUP BY dt ORDER BY dt;
```

```text
┌────────────┬──────────┬──────────────┐
│     dt     │ unpinned │ remote_calls │
│    date    │  int64   │    int64     │
├────────────┼──────────┼──────────────┤
│ 2026-10-20 │        3 │           18 │
│ 2026-10-21 │        1 │           18 │
└────────────┴──────────┴──────────────┘
```

Because the tables hold the commit and the module call detail, you can also ask what a gauge cannot: which repos still call a given module at a given version, or how the mix of backends and version constraints shifts over time.

!!! tip

    Aggregate from [saved facts](../reference/facts.md) to backfill. Changing a weight or a label and re-running `aggregate` over old facts regenerates every sink without scanning anything again.
