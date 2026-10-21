# Prometheus

Get tfmetrics gauges into Prometheus, either by pushing each run to a Pushgateway or by serving the OpenMetrics file for Prometheus to scrape, then query them.

tfmetrics runs as a batch job and exits, so Prometheus cannot scrape it directly. The `openmetrics` sink bridges the gap in one of two ways:

| | Pushgateway | Static file |
| --- | --- | --- |
| How | Each run pushes to a Pushgateway; Prometheus scrapes the gateway | Each run writes `metrics.prom`; any web server serves it; Prometheus scrapes the file |
| Needs | A Pushgateway | Somewhere to host one file |
| Stale series | Replaced on every push | Replaced on every write |

Both keep the same labels: `repo`, `owner` and `org` on every per-repo gauge, plus any [promoted properties](../configuration.md#labels).

## 1. Write the file

Start without any infrastructure. With no options, the sink writes `metrics.prom` into `--out`:

```console
$ tfmetrics aggregate facts/ --offline --sink openmetrics --out metrics/
aggregated 2 repos (2 with Terraform) into 45 series
$ grep -E "tf_hygiene|tf_repos_total|EOF" metrics/metrics.prom
# HELP tf_hygiene_score Weighted 0-100 hygiene score.
# TYPE tf_hygiene_score gauge
tf_hygiene_score{org="acme",owner="acme/networking",repo="acme/tf-network"} 86.0
tf_hygiene_score{org="acme",owner="acme/platform",repo="acme/infra"} 68.8
# HELP tf_repos_total Repos with at least one Terraform module.
# TYPE tf_repos_total gauge
tf_repos_total{org="acme"} 2.0
# EOF
```

The file is in [OpenMetrics text format](https://prometheus.io/docs/specs/om/open_metrics_spec/) and ends with `# EOF`. Every gauge in the [metrics catalog](../reference/metrics.md) is there, with its help text.

## 2a. Push to a Pushgateway

Point the sink at your Pushgateway in `tfmetrics.yaml`:

```yaml title="tfmetrics.yaml"
sinks:
  - openmetrics:
      pushgateway: http://pushgateway.acme.internal:9091
      job: tfmetrics
```

With `pushgateway` set, the sink pushes instead of writing a file. Run `aggregate` and check the gateway:

```console
$ tfmetrics aggregate facts/
aggregated 2 repos (2 with Terraform) into 45 series
$ curl -s http://pushgateway.acme.internal:9091/metrics | grep -E '^tf_hygiene_score|^push_time_seconds'
push_time_seconds{instance="",job="tfmetrics"} 1.792619678100651e+09
tf_hygiene_score{instance="",job="tfmetrics",org="acme",owner="acme/networking",repo="acme/tf-network"} 86
tf_hygiene_score{instance="",job="tfmetrics",org="acme",owner="acme/platform",repo="acme/infra"} 68.8
```

Each push replaces the whole `tfmetrics` group. A repo you delete, or a label value that disappears, drops out on the next run instead of lingering at its last value.

Then have Prometheus scrape the gateway. `honor_labels: true` keeps the `job` label from the push instead of replacing it with the scrape job's name:

```yaml title="prometheus.yml"
scrape_configs:
  - job_name: pushgateway
    honor_labels: true
    static_configs:
      - targets: ["pushgateway.acme.internal:9091"]
```

## 2b. Or serve the file

If you would rather not run a Pushgateway, publish `metrics.prom` from any static web server (an object-store website, nginx, an internal artifact host) and scrape it. A plain web server does not send the OpenMetrics content type, so tell Prometheus 3 what to expect with `fallback_scrape_protocol`:

```yaml title="prometheus.yml"
scrape_configs:
  - job_name: tfmetrics
    metrics_path: /metrics.prom
    honor_labels: true
    fallback_scrape_protocol: OpenMetricsText1.0.0
    static_configs:
      - targets: ["metrics.acme.internal"]
```

`promtool check config prometheus.yml` validates the file before you reload Prometheus.

## 3. Query it

Within one scrape interval the gauges are queryable. The average hygiene score per owner, lowest first:

```promql
sort(avg by (owner) (tf_hygiene_score))
```

```json
[
  { "metric": { "owner": "acme/platform" },   "value": [1792619696.362, "68.8"] },
  { "metric": { "owner": "acme/networking" }, "value": [1792619696.362, "86"] }
]
```

The [dashboards guide](../guides/dashboards.md#queries-to-start-from) has more queries to start a Grafana board from.

## 4. Alert when the job stops

The gauges have no timestamps, so they read as "as of the last run", and a broken nightly job looks exactly like a quiet week. With a Pushgateway, alert on the push time:

```promql
time() - push_time_seconds{job="tfmetrics"} > 2 * 86400
```

With a served file, alert on the scrape instead: `up{job="tfmetrics"} == 0` catches a missing file, and the file's own age is best checked where it is published.

## Keep cardinality in check

`tf_resources_declared` has one series per repo, provider and resource type, which can reach tens of thousands of series across a large estate. Drop the labels you do not chart before they reach Prometheus:

```yaml title="tfmetrics.yaml"
labels:
  drop: [resource_type]
```

Series that become identical are summed, and the hygiene score is averaged, so totals stay correct. The [Parquet sink](parquet.md) keeps the full detail for questions a dashboard does not need to answer.
