# PR check

Scan the repo a pull request changes and show its hygiene score, unpinned modules and parse errors on the PR, with a few lines of workflow and no tokens beyond the one Actions already gives you.

`tfmetrics scan . --sink markdown` parses the checked-out repo and prints a report to stdout. Nothing is cloned and no network is needed beyond your own checkout.

## Try it locally

```console
$ tfmetrics scan . --sink markdown
scanned acme/infra: 5 modules -> facts/acme__infra.json
### tfmetrics: acme/infra

**Hygiene score: 65.3 / 100** · 5 modules (2 root, 3 child) · 8 resources · 8 module calls (1 unpinned) · 1 parse errors

| Check | Passed | Weight |
| --- | --- | --- |
| Remote module calls pinned | 5/6 | 30 |
| Variables and outputs described | 4/8 | 15 |
| Providers with a version constraint | 2/5 | 15 |
| Roots with required_version | 1/2 | 15 |
| Variables with a type | 3/4 | 15 |
| Modules that parse cleanly | 4/5 | 10 |

<details><summary>Remote module calls</summary>

| Address | Version | Pinned | In |
| --- | --- | --- | --- |
| `app.terraform.io/acme/sqs/aws` | `2.1.0` | exact | `live/prod` |
| `github.com/acme/tf-dns` | `v1.4.0` | exact | `live/prod` |
| `github.com/acme/tf-iam` | — | none | `live/prod` |
| `registry.terraform.io/terraform-aws-modules/eks/aws` | `19.21.0` | exact | `live/prod` |
| `registry.terraform.io/terraform-aws-modules/eks/aws` | `17.24.0` | exact | `live/staging` |
| `registry.terraform.io/terraform-aws-modules/s3-bucket/aws` | `~> 3.15` | range | `live/prod` |

</details>

<details><summary>Diagnostics</summary>

- **error** `broken/main.tf:1`: Unclosed configuration block

</details>
```

The `scanned ...` line goes to stderr; stdout is only the report, so it is safe to redirect.

## Add the workflow

This version writes the report to the job summary and keeps one comment on the PR up to date.

```yaml title=".github/workflows/tfmetrics-pr.yml"
name: tfmetrics

on:
  pull_request:
    paths: ["**.tf", "**.tf.json"]

permissions: {}

jobs:
  report:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version: stable
          cache: false
      - run: go install github.com/rossedman/tfmetrics@latest   # pin a release tag for reproducible runs

      - name: Scan
        run: |
          tfmetrics scan . --out "$RUNNER_TEMP/facts" --sink markdown > report.md
          cat report.md >> "$GITHUB_STEP_SUMMARY"

      - name: Comment on the PR
        if: github.event.pull_request.head.repo.full_name == github.repository
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh pr comment "${{ github.event.pull_request.number }}" --body-file report.md --edit-last --create-if-none
```

`--out "$RUNNER_TEMP/facts"` keeps the facts file out of your workspace. The comment step is skipped for pull requests from forks, whose token is read-only.

!!! tip

    Prefer the job summary alone? The `markdown` sink can append to it directly. Set `sinks: [{markdown: {step_summary: true}}]` in `tfmetrics.yaml` and drop the `cat` line.

## Fail the check below a score

tfmetrics reports, it does not gate: `scan` exits 0 whatever the score. To gate, also write the `json` sink and read the score from `metrics.json`:

```sh
tfmetrics scan . --out "$RUNNER_TEMP/facts" --sink markdown --sink json > report.md
score=$(jq -r '.series[] | select(.name == "tf_hygiene_score") | .value' "$RUNNER_TEMP/facts/metrics.json")
echo "hygiene score: $score"
awk -v s="$score" 'BEGIN { exit (s >= 80) ? 0 : 1 }'
```

Start with a threshold just under your current score and raise it as the score rises. The score is weighted, so [tune the weights](../configuration.md#hygiene) to what you want PRs to enforce, such as `pinned` and `required_version` only.
