# Guardrails for pull requests

Stop new Terraform problems arriving through pull requests. Make them visible, point at the exact line, and when the team is ready, refuse to merge them.

**Use this when** an audit has shown you what "good" looks like and you want to stop sliding back: no new unpinned modules, no modules that follow a branch, no files that do not parse.

## Choose how strict to be

tfmetrics gives you three levels. Most teams start at the first and move down the table as old problems get fixed:

| Level | What happens | Uses | Good for |
| --- | --- | --- | --- |
| **Report** | A comment on the PR with the repo's score and problems | `scan . --sink markdown` | Raising awareness with no friction |
| **Annotate** | Each problem appears on its line in the PR's diff | `scan . --sink sarif` and code scanning | Telling the author exactly what to change |
| **Block** | The PR cannot merge while it adds problems | A ruleset on code scanning results, or a score gate | Holding a standard once you have reached it |

All three run `tfmetrics scan .` on the checked-out pull request: no token beyond the one Actions provides, no network beyond the checkout, and a few seconds of run time.

## 1. See what a PR check would say

Run the scan locally with both sinks. This is cisagov's account repo from [Your first org audit](first-org-audit.md):

```console
$ tfmetrics scan cisagov/cool-accounts --out facts --sink sarif
scanned cisagov/cool-accounts: 9 modules -> facts/cisagov__cool-accounts.json
$ jq -r '.runs[0].results | group_by(.ruleId) | map("\(length)\t\(.[0].ruleId)") | .[]' \
    facts/sarif/cisagov__cool-accounts.sarif
46      tfmetrics/unpinned-module
```

Each result names its rule, its level and its line:

```console
$ jq '.runs[0].results[0]' facts/sarif/cisagov__cool-accounts.sarif
{
  "ruleId": "tfmetrics/unpinned-module",
  "ruleIndex": 2,
  "level": "warning",
  "message": {
    "text": "Module \"disable-inactive-iam-users\" (github.com/cisagov/disable-inactive-iam-users-tf-module) has no version or ref; pin an exact version or tag."
  },
  "locations": [
    {
      "physicalLocation": {
        "artifactLocation": {
          "uri": "audit/disable-inactive-iam-users.tf",
          "uriBaseId": "%SRCROOT%"
        },
        "region": {
          "startLine": 5
        }
      }
    }
  ]
}
```

The rules a PR check can raise:

| Rule | Level | Fix |
| --- | --- | --- |
| `tfmetrics/parse-error` | error | The file does not parse, or repeats a block declared earlier in the module. Terraform would reject it too. |
| `tfmetrics/unpinned-module` | warning | Add `version = "x.y.z"` (registry) or `?ref=vX.Y.Z` (git). |
| `tfmetrics/branch-ref` | warning | Replace `?ref=main` with a tag or a commit SHA. |
| `tfmetrics/parse-warning` | warning | Usually an OpenTofu-only file tfmetrics counts but does not parse. |

## 2. Report and annotate in one workflow

This workflow comments a report on each PR and uploads the SARIF log, so the problems also show on the changed lines. It runs on pushes to `main` too, which gives code scanning a baseline: in a PR, GitHub then shows only the problems the PR adds, not every old one.

```yaml title=".github/workflows/tfmetrics.yml"
name: tfmetrics

on:
  pull_request:
    paths: ["**.tf", "**.tf.json"]
  push:
    branches: [main]

permissions: {}

jobs:
  check:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: read
      pull-requests: write     # the comment
      security-events: write   # the SARIF upload
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
        run: tfmetrics scan . --out "$RUNNER_TEMP/facts" --sink markdown --sink sarif > report.md

      - name: Upload to code scanning
        uses: github/codeql-action/upload-sarif@9b3254085494b55a0f7c4c2b3d2d52cc712ce370 # v4.39.1
        with:
          sarif_file: ${{ runner.temp }}/facts/sarif
          category: tfmetrics

      - name: Comment on the PR
        if: github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh pr comment "${{ github.event.pull_request.number }}" --body-file report.md --edit-last --create-if-none
```

Two things to know:

- **Code scanning** is free on public repos. Private repos need GitHub Code Security.
- **Pull requests from forks** get a read-only token, so they can neither upload SARIF nor comment. The scan still runs and `report.md` still lands in the job log.

Want only one of the two? Keep the `markdown` half for a comment ([PR check](../guides/pr-check.md) has that workflow alone), or the `sarif` half for annotations ([SARIF sink](../sinks/sarif.md)).

## 3. Block merges

Annotations are advice. To make them binding, add a ruleset that requires code scanning results:

1. In the repo (or the org, to cover many repos at once), open **Settings → Rules → Rulesets** and create a branch ruleset that targets your default branch.
2. Turn on **Require code scanning results** and add the tool **tfmetrics**, the tool name in the SARIF log.
3. Choose the alert threshold: **Errors** blocks only Terraform that does not parse; **Errors and warnings** also blocks new unpinned modules and branch refs.

From then on, a pull request that adds an alert at or above the threshold cannot merge until it is fixed, or until someone with permission dismisses the alert with a reason. Alerts that already exist on `main` do not block anything. Fix those at your own pace: the code scanning page lists them all.

!!! tip "Roll it out in steps"

    Start with the threshold at **Errors**, which rarely blocks anyone and catches real breakage. Once the `unpinned-module` alerts on `main` are fixed, raise it to **Errors and warnings**.

### Without code scanning: gate on the score

No code scanning, or you want a minimum overall standard rather than rules about specific lines? Gate on the hygiene score instead. The `json` sink writes it, and a shell step fails the check below a threshold; [PR check](../guides/pr-check.md#fail-the-check-below-a-score) has the steps. A score gate blocks a PR when the repo as a whole is below the bar, including old problems the PR did not touch, so set the threshold just under today's score and raise it over time.

## Next steps

- Catch module calls that fall far behind, too: the SARIF sink's [`min_behind`](../sinks/sarif.md#3-choose-what-counts-as-outdated) reports them as notes.
- See the whole org's progress while the guardrails do their work: [Nightly org scan](../guides/nightly-org-scan.md).
