# Tutorials

Each tutorial starts from a question you are likely to have about your Terraform and walks through answering it from start to finish, with real commands and real output. They build on [Install and first run](../getting-started.md); the [guides](../guides/nightly-org-scan.md) and [reference](../reference/metrics.md) pages go deeper on any one piece.

Every example runs against public repos, so you can follow along before pointing tfmetrics at your own org. Your numbers will differ from the ones shown: those repos keep changing, and so do the registries tfmetrics compares them against.

## Pick a tutorial

| You want to know | Tutorial | What it uses |
| --- | --- | --- |
| What Terraform do we have, where is it weakest, and who owns it? | [Your first org audit](first-org-audit.md) | `scan`, `org`, `explain`, `aggregate`, the `json` and `csv` sinks |
| Who still calls the old version of a module, and are they moving? | [Run a module upgrade campaign](upgrade-campaign.md) | `who-uses`, `--behind`, `tf_module_versions_behind` |
| How far behind are our providers and Terraform itself? | [Keep providers and Terraform current](version-drift.md) | `tf_provider_versions_behind`, `tf_terraform_versions_behind`, `who-uses --provider` |
| How do we stop new problems arriving in pull requests? | [Guardrails for pull requests](pr-guardrails.md) | `scan . --sink markdown`, the `sarif` sink, code scanning rulesets |
| How do we scan hundreds of repos, or one huge monorepo, quickly? | [Scan a large org fast](large-orgs.md) | `org` filters, the cache, `--concurrency`, `ignore_dirs`, component tags |

## Which feature when

tfmetrics has a few ways to get at the same facts. They suit different moments:

| Feature | Use it when | Not when |
| --- | --- | --- |
| `scan` | You have a handful of repos, a local checkout, or one repo in CI. | You want a whole org: `org` discovers repos and caches results. |
| `org` | You want every repo in one or more GitHub orgs, on a schedule. | You only need one repo. |
| `explain` | You are looking at one repo and want to see why its score is what it is, or how far behind each of its modules is. | You want to compare repos: aggregate instead. |
| `aggregate` | You want numbers across many repos: dashboards, rankings, trends. | You need to know *which line* to change: use `who-uses` or SARIF. |
| `who-uses` | You are about to change, upgrade or retire a module or provider and need every caller. | You want an org-wide trend: use the `*_versions_behind` metrics. |
| The `markdown` sink | People should read a report: a PR comment or a job summary. | A tool should act on the results. |
| The `sarif` sink | Problems should appear on the exact line, in code scanning or an editor, and optionally block merges. | You want totals or trends. |
| `openmetrics`, `otlp` | You already watch dashboards and alerts in Prometheus or an OpenTelemetry backend. | You need history beyond your metrics retention: add `parquet`. |
| `parquet`, `csv` | You want history in a warehouse, or a spreadsheet to sort and share. | You want live alerts. |
