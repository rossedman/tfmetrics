# Who uses a module

Before you change, deprecate or upgrade a module, you need every caller: which repo, which module in it, at which version, and who to ask. `tfmetrics who-uses` answers that from the facts files a [nightly scan](nightly-org-scan.md) already writes.

## Find every call

Name the module whole or by its last segments. `eks/aws` matches `registry.terraform.io/terraform-aws-modules/eks/aws`, and its `//subdirectory` calls too:

```console
$ tfmetrics who-uses terraform-aws-modules/eks/aws facts/
registry.terraform.io/terraform-aws-modules/eks/aws: 6 calls in 3 repos
versions: ~> 21.20 (2), 17.24.0 (1), 18.31.2 (1), 19.11.0 (1), ~> 21.0 (1)

REPO                 OWNER          MODULE                         VERSION   PINNED  BEHIND  FILE
acme/k8s-infra       acme/platform  aws/prow-build-cluster         ~> 21.20  range   0       aws/prow-build-cluster/eks.tf:21
acme/k8s-infra       acme/platform  aws/policy-staging             19.11.0   exact   78      aws/policy-staging/eks.tf:17
acme/cloud-platform  acme/webops    vpc/eks                        18.31.2   exact   90      vpc/eks/cluster.tf:293
...
```

`BEHIND` counts the `major.minor` release lines between the version the call selects and the latest, the same way [`tf_module_versions_behind`](../reference/metrics.md#tf_module_versions_behind) does. It needs registry and git tag lookups; `--offline` skips them and drops the column. `FILE` is where the `module` block is, so you can open the line that needs changing.

## Plan an upgrade campaign

`--behind` keeps only the calls at least that many release lines behind: the list of callers still to move, with their owners.

```console
$ tfmetrics who-uses eks/aws facts/ --behind 2 --format csv > eks-upgrades.csv
```

`--format csv` and `--format json` include every field, for a spreadsheet, a tracking issue per owner, or your own tooling. Run it again after each wave of upgrades to see who is left.

## Find who requires a provider

`--provider` searches `required_providers` instead of module calls. A provider is matched by its source: `hashicorp/aws`, or just `aws`. A requirement with no `source` counts as `hashicorp/<name>`, as `terraform init` reads it:

```console
$ tfmetrics who-uses hashicorp/aws facts/ --provider --behind 50
```

The `BEHIND` column is then how far the version each constraint selects trails the provider's latest release, as [`tf_provider_versions_behind`](../reference/metrics.md#tf_provider_versions_behind-and-tf_terraform_versions_behind) counts it. A constraint that only sets a floor, such as `>= 5.0`, selects the latest release and is 0.

For the whole workflow, from finding callers to tracking them until the last one moves, follow [Run a module upgrade campaign](../tutorials/upgrade-campaign.md).
