# Markdown

Write a readable report per repo, with the hygiene score, the checks behind it, remote module calls and parse errors, to stdout, a file, or a GitHub Actions job summary.

## 1. Print a report

With no options the sink prints to stdout, one section per repo:

```console
$ tfmetrics aggregate facts/acme__tf-network.json --offline --sink markdown
aggregated 1 repos (1 with Terraform) into 10 series
### tfmetrics: acme/tf-network

**Hygiene score: 86.0 / 100** · 1 modules (0 root, 1 child) · 2 resources · 0 module calls (0 unpinned) · 0 parse errors

| Check | Passed | Weight |
| --- | --- | --- |
| Remote module calls pinned | 0/0 | 30 |
| Variables and outputs described | 2/5 | 15 |
| Providers with a version constraint | 1/1 | 15 |
| Roots with required_version | 1/1 | 15 |
| Variables with a type | 2/3 | 15 |
| Modules that parse cleanly | 1/1 | 10 |
```

The `aggregated ...` line goes to stderr, so stdout is only the report. Repos with remote module calls or parse errors get collapsible sections listing them.

## 2. Write it to a file and the job summary

```yaml title="tfmetrics.yaml"
sinks:
  - markdown:
      path: report.md
      step_summary: true
```

`path` writes the report to a file instead of stdout. `step_summary: true` also appends it to the file named by `$GITHUB_STEP_SUMMARY`, which GitHub Actions shows on the run's summary page; outside Actions the variable is unset and the option does nothing.

## 3. Comment on pull requests

For a report on every PR, `scan` and `--sink markdown` work together without a separate `aggregate` step. The [PR check guide](../guides/pr-check.md) has the workflow, including updating one comment instead of adding a new one on each push.
