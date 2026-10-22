# Keep providers and Terraform current

See how far every repo's providers and Terraform version lag behind, find the exact constraints holding each one back, and get an alert before the gap turns into a forced migration.

**Use this when** a provider ships a major version (the AWS provider's 6.0, say), when a Terraform version you depend on goes out of support, or as a quarterly review. Old providers miss resources and fixes; old Terraform misses language features and eventually stops getting fixes.

This tutorial uses the facts and `metrics/metrics.json` from [Your first org audit](first-org-audit.md), aggregated without `--offline`.

## 1. Know what is measured

tfmetrics has three drift metrics, and all of them count the same way:

| Metric | Measures | Looked up from |
| --- | --- | --- |
| `tf_module_versions_behind` | Each module a repo calls | The module registry, or git tags |
| `tf_provider_versions_behind` | Each provider a repo requires | The provider registry |
| `tf_terraform_versions_behind` | Terraform itself, from each root's `required_version` | Terraform's release tags |

For each constraint, tfmetrics works out the version `terraform init` would pick and counts the `major.minor` release lines between it and the latest release:

| Constraint | Picks | Behind when the latest is 6.20 |
| --- | --- | --- |
| `= 4.24.0` or `4.24.0` | 4.24.0 | every line from 4.25 to 6.20 |
| `~> 4.12.0` | the newest 4.12.x | every line from 4.13 to 6.20 |
| `~> 5.0` | the newest 5.x | every line from 6.0 to 6.20: 21 |
| `>= 4.0` | the newest release | 0 |

Two consequences are worth knowing before you read the numbers:

- **A floor is never behind.** `>= 4.0` allows the latest release, so it counts as 0 even if your lock file still holds 4.0. tfmetrics reads constraints, not `.terraform.lock.hcl`, so it measures how far behind a repo *can't* move without a code change.
- **Busy projects build big numbers.** The AWS provider ships a minor release most weeks, so a year behind is about 50 lines. Compare a provider across repos, not one provider with another.

## 2. See which providers lag the most

```console
$ jq -r '.series[] | select(.name == "tf_provider_versions_behind")
    | "\(.value)\t\(.labels.repo)\t\(.labels.provider)"' metrics/metrics.json | sort -rn | head -6
239     kubernetes/k8s.io                                hashicorp/google-beta
239     kubernetes/k8s.io                                hashicorp/google
225     kubernetes/k8s.io                                hashicorp/aws
213     ministryofjustice/cloud-platform-infrastructure  hashicorp/aws
69      ministryofjustice/cloud-platform-infrastructure  auth0/auth0
31      ministryofjustice/cloud-platform-infrastructure  hashicorp/kubernetes
```

Each value is the worst constraint for that provider anywhere in the repo, including in child modules. A child module that pins `~> 4.12.0` holds the whole repo back, whatever the root allows.

## 3. Find the constraints holding a provider back

The metric says *that* a repo lags; `who-uses --provider` says *where*. It lists every `required_providers` entry for a provider, with how far behind the version it selects is:

```console
$ tfmetrics who-uses hashicorp/aws facts/ --provider --behind 200
hashicorp/aws: 4 requirements in 2 repos
versions: 4.24.0 (1), ~> 4.12.0 (1), ~> 4.25.0 (1), ~> 4.26.0 (1)

REPO                                             OWNER                     MODULE                                                                   VERSION    BEHIND
kubernetes/k8s.io                                unowned                   infra/aws/terraform/registry-sandbox-k8s-io-image-layers                 ~> 4.12.0  225
ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps  terraform/aws-accounts/cloud-platform-ephemeral-test/vpc                 ~> 4.25.0  212
ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps  terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks/components  4.24.0     213
ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps  terraform/modules/cluster_dns                                            ~> 4.26.0  211
```

Four constraints, all pinned within the 4.x line, explain both `hashicorp/aws` rows from step 2. Those are the module directories to change. Drop `--behind` to see every requirement, including the ones with no constraint at all (they show `-` as their version).

A provider is matched by its source: `hashicorp/aws`, or just `aws`. A `required_providers` entry with no `source` means `hashicorp/<name>`, as it does for `terraform init`.

## 4. Check the Terraform version

`tf_terraform_versions_behind` is per repo: the root module with the oldest `required_version` decides it.

```console
$ jq -r '.series[] | select(.name == "tf_terraform_versions_behind") | "\(.value)\t\(.labels.repo)"' \
    metrics/metrics.json | sort -rn
17      kubernetes/k8s.io
0       ministryofjustice/cloud-platform-infrastructure
0       cisagov/cool-accounts
0       alphagov/govuk-infrastructure
0       18F/cg-provision
```

`terraform-aws-modules/terraform-aws-eks` has no row: it is a module with no root modules, so nothing in it decides a Terraform version. To see the constraints behind a number, read them from the facts file. Only roots count; a child module's `required_version` says what it supports, not what runs it:

```console
$ jq -r '.modules[] | select(.kind == "root") | .required_version' facts/kubernetes__k8s.io.json \
    | sort | uniq -c | sort -rn | head -6
     11 1.12.2
     10
      5 ~> 1.5.0
      5 ~> 1.3.0
      5 1.10.5
      4 ~> 1.6
```

This one repo pins five different Terraform versions across its roots, and 10 roots pin none at all. Exact pins (`1.12.2`) and patch-level pins (`~> 1.5.0`) are what fall behind; `~> 1.6` follows every 1.x release. `tfmetrics explain` shows the same column per module, and `metrics/modules.csv` has it for every repo.

## 5. Alert before it hurts

With metrics in Prometheus (see [Nightly org scan](../guides/nightly-org-scan.md)), alert when drift crosses a line you choose. Thresholds depend on how fast each project releases, so set them per provider:

```yaml title="tfmetrics-alerts.yml"
groups:
  - name: tfmetrics-drift
    rules:
      - alert: AWSProviderFarBehind
        expr: tf_provider_versions_behind{provider="hashicorp/aws"} > 52   # about a year of AWS releases
        for: 1d
        labels:
          severity: ticket
        annotations:
          summary: "{{ $labels.repo }} pins hashicorp/aws {{ $value }} release lines behind"
      - alert: TerraformFarBehind
        expr: tf_terraform_versions_behind > 4
        for: 1d
        labels:
          severity: ticket
        annotations:
          summary: "{{ $labels.repo }} requires a Terraform version {{ $value }} minor releases behind"
```

Both carry the `owner` label, so routing alerts to the right team needs no extra mapping.

## Which tool when

- **The metrics** answer "how bad is it, and is it getting better?" across every repo. Use them on dashboards and in alerts.
- **`who-uses --provider`** answers "which lines do I change?" for one provider. Use it when you start fixing.
- **`explain`** answers "what does this one repo require, and how far behind is each module?". Use it when you are in that repo: it shows every module call's latest release and how many major, minor and patch releases it is behind.

When the fix is moving a module that pins an old provider for you, [Run a module upgrade campaign](upgrade-campaign.md) tracks it to the end.
