# How it works

tfmetrics is a five-stage pipeline with one file format in the middle. Scanning turns repos into facts; aggregation turns facts into metrics. Neither stage runs Terraform.

<figure class="diagram" markdown>
![The tfmetrics pipeline. Discover finds repos in GitHub orgs or a local directory, classify marks each directory root or child, parse reads the HCL without evaluating it, and extract writes one facts JSON file per repo, cached by commit SHA. Aggregate reads the facts files and computes 17 tf_ gauges and a hygiene score, then writes them to the json, openmetrics, otlp, parquet, csv and markdown sinks.](assets/how-it-works.png)
</figure>

## The five stages

**1. Discover.** `tfmetrics org` lists the repos of each configured GitHub org and applies that org's `include` and `exclude` filters on name, topic and custom property. Archived repos and forks are skipped by default. With `--local-dir`, it reads a directory of existing git clones instead and makes no API calls. `tfmetrics scan` skips discovery and takes paths, URLs or `owner/name` directly.

**2. Classify.** Each repo is cloned shallowly, with a sparse checkout of only `*.tf`, `*.tf.json`, `*.tofu`, `*.tofu.json` and `CODEOWNERS`. Every directory that holds Terraform files is a module. It is a **root** if it declares a `backend` or `cloud` block or configures a provider, or matches [`scan.root_overrides`](configuration.md#scan); otherwise it is a **child**. `.terraform`, `examples`, `test`, `tests`, `testdata` and hidden directories are skipped by default ([`scan.ignore_dirs`](configuration.md#scan)).

**3. Parse.** Each module is loaded with HashiCorp's `terraform-config-inspect` library, then the raw HCL is walked for what that library does not expose: `lifecycle` settings, `count` and `for_each`, `moved` and `import` blocks, and whether a provider block is configured. Nothing is evaluated. A `count = var.n` is recorded as "uses count", and a variable's value is never resolved. A file that fails to parse becomes a diagnostic and the scan continues. So does a second `resource`, `data`, `module`, `variable` or `output` block with the same address in one module: Terraform rejects it, so it is an error diagnostic, and only one of the two is counted.

**4. Extract.** The result is one [facts file](reference/facts.md) per repo, `facts/<org>__<repo>.json`. Facts are cached by commit SHA plus a hash of the repo's topics, properties and scan settings, so a repo whose `HEAD` has not moved is not cloned at all.

**5. Aggregate and emit.** `tfmetrics aggregate` reads facts files and computes the 19 gauges of the [metrics catalog](reference/metrics.md) and the hygiene score, then writes them to every [sink](guides/dashboards.md). It also looks up the latest versions of each module, provider and Terraform itself for the `*_versions_behind` gauges unless you pass `--offline`.

## Why facts sit in the middle

The facts file is the only thing aggregation reads. That makes the two halves independent:

- Scan once on a schedule, then re-aggregate with different weights, labels or sinks without cloning anything.
- Run `scan` in each repo's CI and `aggregate` centrally over the collected facts.
- Diff two facts files to see exactly what changed between commits.

## What it does not do

tfmetrics is deliberately narrow:

- It does not run `terraform init`, `plan` or `apply`, read state, or call any cloud API. The only credentials it uses are a GitHub token and, optionally, a private registry token.
- It does not evaluate expressions, resolve variables or download modules. Version drift uses registry and git tag listings, never the module code.
- It is not a linter or a policy engine. The hygiene score summarizes a few structural checks; it does not judge your architecture.
- It discovers repos from GitHub only. Other hosts work through `scan` on a clone or a git URL, or `--local-dir`.
