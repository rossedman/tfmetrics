# Metrics catalog

tfmetrics emits 19 gauges, all prefixed `tf_`. They are computed from [facts files](facts.md), so the same facts always give the same numbers, and every sink carries the same catalog.

## Labels

Every series is labelled `org`, `repo` and `owner` (except `tf_repos_total`, which is per org), plus any custom properties you promote with [`labels.properties`](../configuration.md#labels). The tables below list only the labels a metric adds. Use [`labels.drop`](../configuration.md#labels) to remove labels you do not want; series that collide after the drop are summed.

A repo with no Terraform contributes no series at all, and does not count in `tf_repos_total`.

## Inventory

| Metric | Extra labels | Value |
| --- | --- | --- |
| `tf_repos_total` | none (`org` only) | Repos with at least one Terraform module. |
| `tf_modules_total` | `kind` | Module directories, by `root` or `child`. |
| `tf_module_lines` | `kind` | Lines of Terraform configuration, comments and blanks included. |
| `tf_resources_declared` | `provider`, `resource_type` | Resource blocks declared. A block with `count` or `for_each` is one declaration, not one per instance. |
| `tf_data_sources_declared` | `provider` | Data source blocks declared. |
| `tf_prevent_destroy_resources` | `resource_type` | Resources with the literal `lifecycle { prevent_destroy = true }`. |
| `tf_backend` | `type` | Root modules by backend: `s3`, `gcs`, `remote`, `cloud` and so on, or `none`. |

## Modules and versions

| Metric | Extra labels | Value |
| --- | --- | --- |
| `tf_module_calls` | `address`, `source_kind`, `version` | Module calls, one series per address and version. Local calls are included, with the path as the address. |
| `tf_module_calls_unpinned` | `source_kind` | Remote module calls with no version or ref, per source kind. Local calls are excluded, and kinds with nothing unpinned report 0. |
| `tf_module_versions_behind` | `address` | Distinct newer `major.minor` release lines than the pinned version. See below. |
| `tf_provider_versions_behind` | `provider` | The same count for each provider, from the version its constraint selects. See below. |
| `tf_terraform_versions_behind` | none | The same count for Terraform itself, from the version the roots' `required_version` selects. See below. |
| `tf_provider_constraint` | `provider`, `constraint` | Provider requirements in root modules, by version constraint, or `none`. |
| `tf_required_version` | `constraint` | Root modules by Terraform core version constraint, or `none`. |

`source_kind` is one of `local`, `registry`, `private_registry`, `git`, `http`, `s3`, `gcs` or `unknown`. `address` is normalized, so `terraform-aws-modules/eks/aws` and `registry.terraform.io/terraform-aws-modules/eks/aws` are the same module, and a git source becomes `host/org/repo[//subdir]`. Add your own spellings to [`modules.aliases`](../configuration.md#modules).

### tf_module_versions_behind

How far a repo lags behind the latest release of a module it calls. It is computed only for calls to a registry, a private registry or a git source that have a version or ref, and only when you do not pass `--offline`.

The rule:

1. Take the version the call resolves to: the exact version when it is pinned (`19.21.0`), otherwise the highest release that satisfies the constraint (`~> 3.15` resolves to the newest `3.x`), which is what `terraform init` would pick.
2. List the available releases, ignoring prereleases, and find the latest.
3. Count the **distinct `major.minor` lines** newer than the resolved version, up to and including the latest. Patch releases on the same line do not count.

So `1.2.3` against releases `1.2.9`, `1.3.0`, `1.4.1` and `2.0.0` is **3** behind: the lines `1.3`, `1.4` and `2.0`. A module already on the latest release is 0.

When a repo calls the same address several times, the series holds the largest value. A call with no version, a SHA ref, a version nothing satisfies, or a failed lookup gets no series, so absence means unknown, never zero. Versions come from the registry API for registry sources and from `git ls-remote` tags (those that parse as semver) for git sources. A component tag such as `vpc/v1.2.0` is compared only with the repo's other `vpc/` tags, so a monorepo's modules each get their own drift. Failed lookups print a warning and are skipped.

### tf_provider_versions_behind and tf_terraform_versions_behind

The same rule, applied to the providers and the Terraform version a repo runs on. Both need lookups, so neither is computed with `--offline`.

- **Providers.** Every `required_providers` entry with a version constraint, in any module, is resolved against the versions in its registry. A missing `source` means `hashicorp/<name>`, as it does for `terraform init`. `provider` is the source, with `registry.terraform.io/` dropped: `hashicorp/aws`. When several modules constrain one provider, the series holds the largest value, so a child module stuck on `~> 4.0` shows even when the root allows anything. A constraint that only sets a floor, such as `>= 4.0`, selects the latest release and is 0.
- **Terraform.** Every root module's `required_version` is resolved against Terraform's release tags (`git ls-remote` on `github.com/hashicorp/terraform`). The series holds the largest value across the repo's roots. Child modules are left out: their `required_version` states what the module supports, not what runs it.

A provider or root with no constraint gets no series.

## Hygiene

| Metric | Extra labels | Value |
| --- | --- | --- |
| `tf_variables_untyped` | none | Variables with no `type`. |
| `tf_variables_undescribed` | none | Variables with no `description`. |
| `tf_outputs_undescribed` | none | Outputs with no `description`. |
| `tf_parse_errors` | none | Files with an error diagnostic: a syntax error, or a block that repeats an address declared earlier in the module. A file counts once however many errors it has. |
| `tf_hygiene_score` | none | Weighted score from 0 to 100. See below. |

### Hygiene score

The score is the weighted average of six pass rates, scaled to 0 to 100 and rounded to one decimal. A check with nothing to check counts as fully passed, so a repo with no variables is not punished for typing them.

| Check | Default weight | Pass rate is |
| --- | --- | --- |
| `pinned` | 30 | Remote module calls that have a version or ref, out of all remote calls. |
| `required_version` | 15 | Root modules that set `required_version`, out of all roots. A repo with no roots uses all its modules. |
| `provider_constraints` | 15 | Provider requirements that have a version constraint. A provider that is used but not declared counts as unconstrained. |
| `typed_vars` | 15 | Variables that have a `type`. |
| `described` | 15 | Variables and outputs that have a `description`. |
| `parse_clean` | 10 | Modules in which no file failed to parse. |

Worked through for the example repo in [Install and first run](../getting-started.md#read-the-report):

```text
pinned                30 x 5/6 = 25.00
described             15 x 4/8 =  7.50
provider_constraints  15 x 2/5 =  6.00
required_version      15 x 1/2 =  7.50
typed_vars            15 x 3/4 = 11.25
parse_clean           10 x 4/5 =  8.00
                               -------
                               65.25 / 100  ->  65.3
```

Change the weights with [`hygiene.weights`](../configuration.md#hygiene). They are relative: `pinned: 60` makes pinning worth 60 of 130 points instead of 30 of 100. `tfmetrics explain` always shows the checks behind a score.

## Example output

The OpenMetrics sink for that repo, with `owner` coming from its `CODEOWNERS`. These are real lines from `tfmetrics aggregate facts/ --sink openmetrics`; `tf_module_versions_behind` was looked up against the public registry on 2026-10-21 and will differ when you run it.

```text
tf_hygiene_score{org="acme",owner="acme/platform",repo="acme/infra"} 65.3
tf_modules_total{kind="child",org="acme",owner="acme/platform",repo="acme/infra"} 3.0
tf_modules_total{kind="root",org="acme",owner="acme/platform",repo="acme/infra"} 2.0
tf_module_calls_unpinned{org="acme",owner="acme/platform",repo="acme/infra",source_kind="git"} 1.0
tf_module_calls_unpinned{org="acme",owner="acme/platform",repo="acme/infra",source_kind="registry"} 0.0
tf_required_version{constraint=">= 1.6.0",org="acme",owner="acme/platform",repo="acme/infra"} 1.0
tf_required_version{constraint="none",org="acme",owner="acme/platform",repo="acme/infra"} 1.0
tf_module_versions_behind{address="registry.terraform.io/terraform-aws-modules/eks/aws",org="acme",owner="acme/platform",repo="acme/infra"} 122.0
tf_repos_total{org="acme"} 1.0
```

## Cardinality

`tf_resources_declared` (one series per repo, provider and resource type) and `tf_module_calls` (one per address and version) dominate the series count. Two hundred copies of the example repo produce 7,001 series. If that is too many for your backend, drop `resource_type` and `version` with [`labels.drop`](../configuration.md#labels), or keep the detail in [Parquet](../guides/dashboards.md#trends-with-parquet) and send only rollups to the metrics store.
