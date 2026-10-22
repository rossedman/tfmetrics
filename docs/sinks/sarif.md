# SARIF

Write a [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html) log per repo, so problems show up where people already look: as GitHub code scanning alerts, as annotations on the lines a pull request changes, or in any editor with a SARIF viewer.

## What it reports

| Rule | Level | What |
| --- | --- | --- |
| `tfmetrics/parse-error` | error | A file that does not parse, or a block that repeats an address declared earlier in the module. |
| `tfmetrics/parse-warning` | warning | A file read with a warning, such as an OpenTofu file tfmetrics counts but does not parse. |
| `tfmetrics/unpinned-module` | warning | A remote module call with no version or ref. |
| `tfmetrics/branch-ref` | warning | A git module call whose `?ref=` names a branch rather than a tag or commit. |
| `tfmetrics/outdated-module` | note | A module call at least `min_behind` release lines behind its latest release. Off unless you set `min_behind`, and it needs version lookups, so it never appears from `scan` or with `--offline`. |

Each result points at the file and line of the `module` block or the problem. Facts written by tfmetrics before module calls carried a position point at the module's directory instead.

## 1. Write the logs

```console
$ tfmetrics scan . --out facts --sink sarif
scanned acme/infra: 5 modules -> facts/acme__infra.json
$ ls facts/sarif
acme__infra.sarif
```

There is one file per repo, named like its facts file, in `<out>/sarif` unless `path` says otherwise. Repos with no Terraform get no file.

## 2. Upload it to code scanning

In a pull request workflow, `upload-sarif` turns the results into code scanning alerts. Alerts on lines the pull request changes show as annotations in its diff.

```yaml title=".github/workflows/tfmetrics-sarif.yml"
name: tfmetrics code scanning

on:
  pull_request:
    paths: ["**.tf", "**.tf.json"]
  push:
    branches: [main]

permissions: {}

jobs:
  sarif:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: read
      security-events: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: stable
          cache: false
      - run: go install github.com/rossedman/tfmetrics@latest   # pin a release tag for reproducible runs
      - run: tfmetrics scan . --out "$RUNNER_TEMP/facts" --sink sarif
      - uses: github/codeql-action/upload-sarif@9b3254085494b55a0f7c4c2b3d2d52cc712ce370 # v4.39.1
        with:
          sarif_file: ${{ runner.temp }}/facts/sarif
          category: tfmetrics
```

Code scanning needs GitHub Advanced Security on private repos; on public repos it is free.

## 3. Choose what counts as outdated

```yaml title="tfmetrics.yaml"
sinks:
  - sarif:
      path: alerts/
      min_behind: 3
```

`min_behind` is how many `major.minor` release lines behind a call must be before it is reported, counted as [`tf_module_versions_behind`](../reference/metrics.md#tf_module_versions_behind) counts them. It defaults to `0`, which turns the rule off. Across 93 public repos, 4,382 module calls were at least 1 line behind, 624 at least 5 and 321 at least 10, so start high and lower it as the alerts get fixed. A module that ships a minor release every week reaches 10 sooner than one that ships every quarter. The rule only has data to work with when versions are looked up: `org` with `--sink sarif`, or `aggregate` without `--offline`.
