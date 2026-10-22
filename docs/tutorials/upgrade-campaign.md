# Run a module upgrade campaign

Move every caller of a module to a new version: find them all, hand the work to the people who own it, and track the campaign until nobody is left behind.

**Use this when** a module you depend on ships a major version, when a version has a security fix everyone needs, or when you want to retire a module altogether. The same steps work for public registry modules, private registry modules and modules in your own git repos.

This tutorial uses the facts from [Your first org audit](first-org-audit.md). Any facts directory works: a nightly scan's output is ideal.

## 1. Find every caller

`who-uses` takes a module address, whole or by its last segments, and lists every call to it:

```console
$ tfmetrics who-uses terraform-aws-modules/eks/aws facts/
terraform-aws-modules/eks/aws: 7 calls in 3 repos
versions: ~> 21.20 (3), 17.24.0 (1), 18.31.2 (1), 19.11.0 (1), ~> 21.0 (1)

ADDRESS                                                                 REPO                                             OWNER                                                                                                MODULE                                                        VERSION   PINNED  BEHIND  FILE
registry.terraform.io/terraform-aws-modules/eks/aws                     alphagov/govuk-infrastructure                    alphagov/gov-uk-platform-engineering,alphagov/govuk-data-engineering,alphagov/national-data-library  terraform/deployments/cluster-infrastructure                  ~> 21.0   range   0       terraform/deployments/cluster-infrastructure/main.tf:239
registry.terraform.io/terraform-aws-modules/eks/aws                     kubernetes/k8s.io                                unowned                                                                                              infra/aws/terraform/kops-infra-ci                             ~> 21.20  range   0       infra/aws/terraform/kops-infra-ci/eks.tf:17
registry.terraform.io/terraform-aws-modules/eks/aws                     kubernetes/k8s.io                                unowned                                                                                              infra/aws/terraform/policy-staging-account-1                  19.11.0   exact   78      infra/aws/terraform/policy-staging-account-1/eks.tf:17
registry.terraform.io/terraform-aws-modules/eks/aws                     kubernetes/k8s.io                                unowned                                                                                              infra/aws/terraform/prow-build-cluster                        ~> 21.20  range   0       infra/aws/terraform/prow-build-cluster/eks.tf:21
registry.terraform.io/terraform-aws-modules/eks/aws                     ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps                                                                             terraform/aws-accounts/cloud-platform-aws/vpc/eks             18.31.2   exact   90      terraform/aws-accounts/cloud-platform-aws/vpc/eks/cluster.tf:293
registry.terraform.io/terraform-aws-modules/eks/aws                     ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps                                                                             terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks  17.24.0   exact   122     terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks/cluster.tf:120
registry.terraform.io/terraform-aws-modules/eks/aws//modules/karpenter  kubernetes/k8s.io                                unowned                                                                                              infra/aws/terraform/prow-build-cluster                        ~> 21.20  range   0       infra/aws/terraform/prow-build-cluster/karpenter.tf:17
```

The headline answers "how big is this?": 7 calls in 3 repos, and which versions they ask for. Calls to the module's submodules (`//modules/karpenter`) count too, because they upgrade with it.

Each row says where the call is (`FILE` is the `module` block's line), who owns the repo, and `BEHIND`: how many `major.minor` release lines the version it uses trails the latest by. A constraint like `~> 21.20` resolves to the newest 21.x release, the way `terraform init` would, so it is 0 behind until 22.0 ships.

## 2. Decide what "done" looks like

Pick the oldest version you are willing to leave in place, and turn it into a `--behind` threshold. Here the module's 21.x calls are current and anything from 19.x or older is the problem, so 25 lines behind separates them cleanly:

```console
$ tfmetrics who-uses terraform-aws-modules/eks/aws facts/ --behind 25
registry.terraform.io/terraform-aws-modules/eks/aws: 3 calls in 2 repos
versions: 17.24.0 (1), 18.31.2 (1), 19.11.0 (1)

REPO                                             OWNER                     MODULE                                                        VERSION  PINNED  BEHIND  FILE
kubernetes/k8s.io                                unowned                   infra/aws/terraform/policy-staging-account-1                  19.11.0  exact   78      infra/aws/terraform/policy-staging-account-1/eks.tf:17
ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps  terraform/aws-accounts/cloud-platform-aws/vpc/eks             18.31.2  exact   90      terraform/aws-accounts/cloud-platform-aws/vpc/eks/cluster.tf:293
ministryofjustice/cloud-platform-infrastructure  ministryofjustice/WebOps  terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks  17.24.0  exact   122     terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks/cluster.tf:120
```

That is the campaign: three module blocks to change, in two repos. Calls with no version at all are left out of `--behind` lists, because there is no version to measure; find them with the unfiltered list, or with `tf_module_calls_unpinned`.

!!! note "Why the numbers are large"

    `BEHIND` counts every minor release, so a module that ships a minor version every few weeks builds up big numbers quickly. Compare calls of the same module with each other, not with other modules.

## 3. Hand out the work

Export the list and give each owner theirs. CSV opens in a spreadsheet:

```console
$ tfmetrics who-uses terraform-aws-modules/eks/aws facts/ --behind 25 --format csv > eks-upgrades.csv
$ cat eks-upgrades.csv
address,repo,owner,module,file,line,source_kind,version,pinned,behind
registry.terraform.io/terraform-aws-modules/eks/aws,kubernetes/k8s.io,unowned,infra/aws/terraform/policy-staging-account-1,infra/aws/terraform/policy-staging-account-1/eks.tf,17,registry,19.11.0,exact,78
registry.terraform.io/terraform-aws-modules/eks/aws,ministryofjustice/cloud-platform-infrastructure,ministryofjustice/WebOps,terraform/aws-accounts/cloud-platform-aws/vpc/eks,terraform/aws-accounts/cloud-platform-aws/vpc/eks/cluster.tf,293,registry,18.31.2,exact,90
registry.terraform.io/terraform-aws-modules/eks/aws,ministryofjustice/cloud-platform-infrastructure,ministryofjustice/WebOps,terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks,terraform/aws-accounts/cloud-platform-ephemeral-test/vpc/eks/cluster.tf,120,registry,17.24.0,exact,122
```

To open one tracking issue per repo instead, use the JSON output. This sketch files them in the repo that owns the work, with a checklist of every module block to change:

```sh
tfmetrics who-uses terraform-aws-modules/eks/aws facts/ --behind 25 --format json |
  jq -c 'group_by(.repo)[]' |
  while read -r calls; do
    repo=$(jq -r '.[0].repo' <<<"$calls")
    body=$(jq -r '"Upgrade terraform-aws-modules/eks/aws to 21.x in:\n\n" +
      (map("- [ ] `\(.file):\(.line)` (now \(.version), \(.behind) release lines behind)") | join("\n"))' <<<"$calls")
    gh issue create --repo "$repo" --title "Upgrade the EKS module to 21.x" --body "$body"
  done
```

## 4. Track progress

Run the same command after each nightly scan. The headline is your burndown: when `--behind 25` prints `no uses found`, the campaign is done.

If you send metrics to Prometheus (see [Nightly org scan](../guides/nightly-org-scan.md)), the same story is on a dashboard. `tf_module_versions_behind` holds, per repo, how far its oldest call to each module is behind, and `tf_module_calls` counts calls per version:

```promql
# Repos still running a call 25 or more release lines behind
count(tf_module_versions_behind{address="registry.terraform.io/terraform-aws-modules/eks/aws"} >= 25)

# Calls per version, to watch old versions drain away
sum by (version) (tf_module_calls{address="registry.terraform.io/terraform-aws-modules/eks/aws"})
```

## 5. Keep it done

Once the old versions are gone, stop them coming back. The [`sarif` sink](../sinks/sarif.md) can flag outdated module calls as code scanning alerts on the line that needs changing, but it is off by default because most orgs have thousands of calls a release or two behind. Turn it on with a threshold well above that noise, such as `min_behind: 25`, once the campaign has brought the count down. [Guardrails for pull requests](pr-guardrails.md) shows the rest of the setup.

## Variations

- **Retiring a module.** Every caller has to move, whatever version they are on, so skip `--behind` and versions altogether: `tfmetrics who-uses github.com/acme/tf-legacy-vpc facts/ --offline` lists every call without looking anything up.
- **Your own git modules.** Address them as `host/org/repo`, such as `github.com/acme/tf-network`. Tags like `v1.4.0` (or `tags/1.4.0`) count as versions. In a monorepo that tags each module separately (`vpc/v1.2.0`, `vpc-v1.2.0`), each module is compared only with its own tags.
- **Private registries.** Lookups need a token: list each registry under [`modules.registries`](../configuration.md#modules) with its `token_env`, or set Terraform's `TF_TOKEN_<host>` variables. Without one, `BEHIND` shows `-` for those calls.
- **One module, several spellings.** If some repos call a module by its registry address and others by its git URL, map one to the other with [`modules.aliases`](../configuration.md#modules) so `who-uses` and the metrics treat them as one module.
