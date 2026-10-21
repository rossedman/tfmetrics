# Parquet

Write one Parquet table of modules and one of module calls on every run, keep them partitioned by day, and answer trend questions with SQL.

Gauges answer "what is true now". Questions like "how fast are teams pinning their modules?" or "who still calls version 17 of the EKS module?" need rows that keep the commit and every call. The `parquet` sink writes them.

## 1. Write the tables

```console
$ tfmetrics aggregate facts/ --offline --sink parquet --out tables/
aggregated 2 repos (2 with Terraform) into 45 series
$ ls tables/
module_calls.parquet  modules.parquet
```

| Table | One row per | Columns |
| --- | --- | --- |
| `modules.parquet` | module directory | `repo`, `org`, `owner`, `commit`, `scanned_at`, `path`, `kind`, `files`, `lines`, `required_version`, `backend`, `providers`, `providers_constrained`, `resources`, `data_sources`, `module_calls`, `variables`, `variables_untyped`, `variables_undescribed`, `outputs`, `outputs_undescribed`, `prevent_destroy`, `moved_blocks`, `import_blocks`, `parse_errors` |
| `module_calls.parquet` | module call | `repo`, `org`, `owner`, `commit`, `scanned_at`, `module_path`, `name`, `source`, `source_kind`, `address`, `version`, `pinned`, `count`, `for_each` |

`scanned_at` is a millisecond timestamp, and the counts are 64-bit integers. The [facts reference](../reference/facts.md) defines each field.

## 2. Query them with DuckDB

[DuckDB](https://duckdb.org) reads Parquet in place, with nothing to load. Every remote module address and the versions in use:

```sql
SELECT address, version, pinned, count(*) AS calls
FROM 'tables/module_calls.parquet'
WHERE source_kind <> 'local'
GROUP BY ALL ORDER BY address, version;
```

```text
┌───────────────────────────────────────────────────────────┬─────────┬─────────┬───────┐
│                          address                          │ version │ pinned  │ calls │
│                          varchar                          │ varchar │ varchar │ int64 │
├───────────────────────────────────────────────────────────┼─────────┼─────────┼───────┤
│ app.terraform.io/acme/sqs/aws                             │ 2.1.0   │ exact   │     1 │
│ github.com/acme/tf-dns                                    │ v1.4.0  │ exact   │     1 │
│ github.com/acme/tf-iam                                    │         │ none    │     1 │
│ registry.terraform.io/terraform-aws-modules/eks/aws       │ 17.24.0 │ exact   │     1 │
│ registry.terraform.io/terraform-aws-modules/eks/aws       │ 19.21.0 │ exact   │     1 │
│ registry.terraform.io/terraform-aws-modules/s3-bucket/aws │ ~> 3.15 │ range   │     1 │
└───────────────────────────────────────────────────────────┴─────────┴─────────┴───────┘
```

## 3. Keep history in object storage

Write each run to its own `dt=` partition, then upload it. The sink writes local paths only:

```sh
day=$(date -u +%F)
tfmetrics aggregate facts/ --sink parquet --out "tables/dt=$day"
aws s3 cp --recursive "tables/dt=$day" "s3://acme-data/tfmetrics/dt=$day"
# or: gcloud storage cp --recursive "tables/dt=$day" "gs://acme-data/tfmetrics/"
```

DuckDB reads every partition at once with `hive_partitioning`, which turns `dt` into a column. Unpinned remote calls per day:

```sql
SELECT dt,
       count(*) FILTER (WHERE pinned = 'none') AS unpinned,
       count(*) AS remote_calls
FROM read_parquet('tables/dt=*/module_calls.parquet', hive_partitioning = true)
WHERE source_kind <> 'local'
GROUP BY dt ORDER BY dt;
```

The [dashboards guide](../guides/dashboards.md#trends-with-parquet) shows this query's output across two days.

## 4. Load into a warehouse

Any engine that reads Parquet works the same way. In BigQuery, load each day into a table partitioned by load date:

```sh
bq load --source_format=PARQUET --time_partitioning_type=DAY \
  acme_metrics.tfmetrics_module_calls "gs://acme-data/tfmetrics/dt=$day/module_calls.parquet"
```

Snowflake, Athena, Spark and pandas read the same files with their own Parquet readers.

!!! tip

    Backfill from saved [facts files](../reference/facts.md). `aggregate` over an archived `facts/` directory rebuilds that day's tables without cloning or scanning anything.
