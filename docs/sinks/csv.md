# CSV

Write the module and module-call tables as CSV, for a spreadsheet, a quick `grep`, or a tool that does not read Parquet.

The `csv` sink writes the same two tables as the [Parquet sink](parquet.md#1-write-the-tables), with the same columns, as plain CSV with a header row.

## 1. Write the files

```console
$ tfmetrics aggregate facts/ --offline --sink csv --out tables/
aggregated 2 repos (2 with Terraform) into 45 series
$ head -3 tables/module_calls.csv
repo,org,owner,commit,scanned_at,module_path,name,source,source_kind,address,version,pinned,count,for_each
acme/infra,acme,acme/platform,61f1659ced23a89c2ce11ca7611d3661976ed800,2026-10-21T21:54:19Z,live/prod,dns,git::https://github.com/acme/tf-dns.git?ref=v1.4.0,git,github.com/acme/tf-dns,v1.4.0,exact,false,false
acme/infra,acme,acme/platform,61f1659ced23a89c2ce11ca7611d3661976ed800,2026-10-21T21:54:19Z,live/prod,eks,terraform-aws-modules/eks/aws,registry,registry.terraform.io/terraform-aws-modules/eks/aws,19.21.0,exact,false,false
```

- `modules.csv`: one row per module directory.
- `module_calls.csv`: one row per module call.

Timestamps are RFC 3339 in UTC, booleans are `true` and `false`, and empty values (a missing `version` or `backend`) are empty cells.

## 2. Open it

Import either file into Google Sheets or Excel as-is; the header row becomes the column names. A pivot table on `module_calls.csv` with `address` as rows and `version` as columns shows every module's version spread at a glance.

## 3. Or query it

DuckDB, `csvkit` and `pandas` all read the files directly. Modules, resources and hygiene gaps per owner:

```sql
SELECT owner, count(*) AS modules, sum(resources) AS resources,
       sum(variables_untyped) AS untyped_vars, sum(parse_errors) AS parse_errors
FROM 'tables/modules.csv' GROUP BY owner ORDER BY owner;
```

```text
┌─────────────────┬─────────┬───────────┬──────────────┬──────────────┐
│      owner      │ modules │ resources │ untyped_vars │ parse_errors │
│     varchar     │  int64  │  int128   │    int128    │    int128    │
├─────────────────┼─────────┼───────────┼──────────────┼──────────────┤
│ acme/networking │       1 │         2 │            1 │            0 │
│ acme/platform   │       4 │         7 │            1 │            0 │
└─────────────────┴─────────┴───────────┴──────────────┴──────────────┘
```

For history across runs, prefer [Parquet](parquet.md): it keeps column types and is much smaller.
