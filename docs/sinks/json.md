# JSON

Write the whole run as JSON: every metric series in `metrics.json`, and a copy of every facts file next to it, so one directory is a complete, archivable record of the run.

`json` is the default sink: `aggregate` writes it when no `--sink` is given and the config lists no `sinks`.

## 1. Write a run

```console
$ tfmetrics aggregate facts/ --offline --sink json --out metrics/
aggregated 2 repos (2 with Terraform) into 45 series
$ find metrics | sort
metrics
metrics/facts
metrics/facts/acme__infra.json
metrics/facts/acme__tf-network.json
metrics/metrics.json
```

`metrics.json` holds the time of the run, the number of repos with Terraform, and every series with its name, labels and value:

```json title="metrics/metrics.json"
{
  "generated_at": "2026-10-21T21:54:30Z",
  "repos": 2,
  "series": [
    {
      "name": "tf_backend",
      "labels": {
        "org": "acme",
        "owner": "acme/platform",
        "repo": "acme/infra",
        "type": "cloud"
      },
      "value": 1
    },
```

The series are the same ones every other gauge sink sends, after [`labels`](../configuration.md#labels) are applied. The facts files are the [facts schema](../reference/facts.md) unchanged.

## 2. Use it from scripts

Hygiene score per repo:

```console
$ jq -r '.series[] | select(.name=="tf_hygiene_score") | "\(.labels.repo)\t\(.value)"' metrics/metrics.json
acme/tf-network	86
acme/infra	68.8
```

How many repos have at least one unpinned module call:

```console
$ jq '[.series[] | select(.name=="tf_module_calls_unpinned" and .value > 0)] | length' metrics/metrics.json
1
```

Facts carry detail no metric does, such as each root module's backend and `required_version`:

```console
$ jq -r '.modules[] | select(.kind=="root") | "\(.path)\t\(.backend)\t\(.required_version)"' metrics/facts/acme__infra.json
live/prod	s3	>= 1.6.0
live/staging	cloud
```

## 3. Archive every run

Upload the directory after each run, for example as a workflow artifact or to `s3://acme-data/tfmetrics/runs/$(date -u +%F)/`. Because the facts are inside, any archived run can be re-aggregated later with new labels, weights or sinks.
