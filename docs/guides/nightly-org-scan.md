# Nightly org scan

Scan every Terraform repo in your GitHub org on a schedule and publish one consistent set of metrics, with a GitHub Actions workflow that reuses its cache so unchanged repos cost almost nothing.

You need a token that can read the repos, an org name, and a place to send the metrics. The example pushes to a [Pushgateway](../sinks/openmetrics.md#2a-push-to-a-pushgateway) and keeps the [Parquet](dashboards.md#trends-with-parquet) tables as a build artifact.

## 1. Create a token

`GITHUB_TOKEN` in Actions cannot read other repos, so create a fine-grained personal access token or a GitHub App installation token for the org with:

- **Contents: read** and **Metadata: read** on the repos to scan.
- **Custom properties: read** on the org, only if you filter on custom properties or promote them to labels. Without it, property lookups return nothing instead of failing.

Store it as the repository secret `TFMETRICS_GITHUB_TOKEN`. The workflow below exposes it as `GITHUB_TOKEN`, the variable tfmetrics reads by default (change that with [`source.github.token_env`](../configuration.md#sourcegithub)).

## 2. Write the config

Commit a `tfmetrics.yaml` next to the workflow. This one scans repos tagged `terraform` or named `tf-*`, skips sandboxes, and groups by team.

```yaml title="tfmetrics.yaml"
source:
  github:
    orgs:
      - name: acme
        include:
          topics: [terraform]
        exclude:
          names: ["*-sandbox"]

labels:
  properties: [team]
  drop: [resource_type]   # keeps series counts manageable

sinks:
  - openmetrics:
      pushgateway: http://pushgateway.acme.internal:9091
      job: tfmetrics
  - parquet:
      path: metrics/
```

Run `tfmetrics org --dry-run` locally first. It prints every repo the filters select, with its topics and properties, and clones nothing.

## 3. Add the workflow

```yaml title=".github/workflows/tfmetrics.yml"
name: tfmetrics

on:
  schedule:
    - cron: "17 3 * * *"
  workflow_dispatch:

permissions: {}

concurrency:
  group: tfmetrics
  cancel-in-progress: false

jobs:
  scan:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: stable
          cache: false
      - run: go install github.com/rossedman/tfmetrics@latest   # pin a release tag for reproducible runs

      # Facts are cached by repo and commit, so only changed repos are cloned.
      - uses: actions/cache@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0
        with:
          path: ~/.cache/tfmetrics
          key: tfmetrics-${{ github.run_id }}
          restore-keys: tfmetrics-

      - name: Scan the org
        env:
          GITHUB_TOKEN: ${{ secrets.TFMETRICS_GITHUB_TOKEN }}
        run: tfmetrics org --out facts/ --strict

      - name: Aggregate and publish
        env:
          GITHUB_TOKEN: ${{ secrets.TFMETRICS_GITHUB_TOKEN }}
        run: tfmetrics aggregate facts/

      - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: tfmetrics-${{ github.run_id }}
          path: |
            facts/
            metrics/
          retention-days: 30
```

What each part does:

- **Split commands.** `org` writes facts; `aggregate` turns them into metrics. Running them separately keeps the facts as an artifact you can re-aggregate later with different labels or weights, without cloning anything.
- **Cache.** A repo whose `HEAD` matches the cache reuses its last facts without a clone, and prints `cached`. The cache key changes every run so the cache is always refreshed, and `restore-keys` restores the latest. Changing scan settings rescans automatically.
- **`--strict`** makes the job fail if any repo fails to scan. Without it, failures are listed in the log and the run succeeds unless every repo failed.
- **`aggregate` without `--offline`** looks up the latest versions of every module for `tf_module_versions_behind`. Private registries need [`modules.registry_token_env`](../configuration.md#modules).

## Reading the log

Each repo prints one line as it finishes, in whatever order the parallel scans complete, then a summary. This is a second run over three repos where one has a new commit:

```console
$ tfmetrics org --org acme --local-dir ./clones --out facts/
cached  acme/tf-compute
cached  acme/tf-network
scanned acme/tf-data
3 repos: 1 scanned, 2 from cache, 0 failed -> facts/
time: 5ms wall; summed across workers: ls-remote 0s (0), clone 0s (0), parse 1ms
$ tfmetrics aggregate facts/ --offline
aggregated 3 repos (3 with Terraform) into 106 series
```

`--local-dir` scans a directory of existing clones instead of calling GitHub. Against GitHub the output has the same shape plus a `github api:` line with the requests made, retries, time spent waiting and the rate limit left in each bucket. A repo that fails to clone or scan prints `error   <repo>: <reason>` and is counted under `failed`.

The `time:` line shows where a run spent its time. Durations after "summed across workers" add up every worker's time, so they can exceed the wall-clock time; compare them with each other to find the bottleneck.

## Rate limits and speed

At org scale the cost of a run is network round trips, not parsing: parsing a 4,000-file monorepo takes under two seconds, while every git or API call is a few hundred milliseconds of latency. tfmetrics keeps those calls few and within GitHub's policy:

- **Discovery is a handful of API calls.** Repos and custom property values come 100 per page, and each repo's default-branch `HEAD` comes from one GraphQL query per 100 repos (about one point of the 5,000-point hourly budget). A 1,000-repo org costs roughly 30 calls.
- **Unchanged repos cost nothing more.** The `HEAD` from discovery is checked against the cache with no git call, so a nightly run where most repos did not change makes no clones at all. A run with `--ref` falls back to one `git ls-remote` per repo.
- **API calls follow GitHub's rules.** Requests go out one at a time. An exhausted primary limit waits for its `x-ratelimit-reset`; a secondary limit honors `Retry-After`, or waits a minute and doubles; server errors and dropped connections back off exponentially with jitter. Each wait is printed as `github api: <reason>; waiting <duration>`. `source.github.max_retries` caps the retries, and a negative value fails fast.
- **Clones are cheap and retried.** Each clone is shallow, with a sparse checkout of only `*.tf`, `*.tf.json`, `*.tofu` and `CODEOWNERS`; on `hashicorp/terraform-provider-aws` that is 44 MB and 2 s instead of 474 MB and 11 s. Throttled or dropped clones (429, 5xx, connection resets) are retried up to three times with backoff. Clones run `scan.concurrency` at a time (16 by default); raise it on a runner with more bandwidth.
- **Version lookups run in parallel.** `aggregate` looks up each distinct module address once, eight at a time, with the same backoff on throttling.

Clones go to the system temp directory. On a runner with slow disks, point `TMPDIR` at a tmpfs such as `/dev/shm` to keep them in memory; a sparse clone of a typical module repo is about 1 MB.

## Several orgs

List more orgs under `source.github.orgs`, each with its own filters. The `org` label keeps them apart. One token must be able to read all of them, or run one job per org with `--org` and aggregate the combined `facts/` directories.
