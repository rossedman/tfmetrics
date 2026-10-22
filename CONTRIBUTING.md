# Contributing to tfmetrics

Thanks for helping. tfmetrics statically parses Terraform repos and emits org-wide metrics: inventory, version drift, module adoption and hygiene, without `terraform init`, plan or state. Read the [docs](https://rossedman.io/tfmetrics/) first for what each command does.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Report security problems privately, as described in [SECURITY.md](SECURITY.md).

## Dev setup

You need Go (the version in `go.mod`), `make` and `git`. `make check` also needs golangci-lint v2, GoReleaser v2 and [uv](https://docs.astral.sh/uv/) (for the docs build).

```sh
git clone https://github.com/rossedman/tfmetrics.git
cd tfmetrics
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
go install github.com/goreleaser/goreleaser/v2@latest
make build
./bin/tfmetrics version
```

govulncheck is a tool dependency in `go.mod`, so `make vuln` runs it with `go tool govulncheck` and nothing needs installing.

Useful targets: `make test` (race detector), `make cover` (coverage gate), `make bench`, `make fuzz`, `make lint` (also enforces gofmt and goimports), `make docs-serve` (live docs preview).

## Run every check

```sh
make check
```

This is exactly what CI runs: build, lint, tests with the race detector and coverage gate, command-layout and doc-comment checks, benchmark and fuzz smoke runs, govulncheck, a strict docs build, and a GoReleaser snapshot. Run it before you open a PR.

## Quality bars

- 90% test coverage on hand-written code. Generated files and `examples/` are excluded from the gate (`make cover`).
- Benchmarks on critical paths, so regressions show up in `make bench` and `make bench-smoke`.
- Fuzz targets on input-handling critical functions (parsers, config and facts decoding). Run them with `make fuzz`.
- A doc comment on every function and method, checked by `make doccheck`.
- One cobra command per file in `cmd/`, checked by `make check-cmds`.
- Docs change with behavior. Command reference pages are generated (`make docs`); do not edit them by hand.

## Critical paths

These functions parse untrusted input, transform data, or run once per repo, module or series. Each one has a benchmark; the ones that take external input also have a fuzz target. Keep both when you change them.

| Function | Why it is critical | Benchmark | Fuzz |
| --- | --- | --- | --- |
| `source.Normalizer.Classify`, `source.Pinning`, `source.TagVersion`, `source.ProviderAddress` | Parse every module source, git ref and provider source as written in someone's repo | yes | yes |
| `usage.Find`, `usage.Match` | Match a user-typed module or provider against every call in an org's facts | yes | yes |
| SARIF log building (`sink.SARIF`) | Runs over every module call and diagnostic of every repo | yes | |
| `parse.Module` | Parses arbitrary HCL and JSON from every module directory | yes | yes |
| `classify.FindModuleDirs` | Walks every file of every cloned repo | yes | |
| `owners.FromCodeowners` | Parses CODEOWNERS files from every repo | yes | yes |
| `filter.Compile`, `filter.Selector.Selected` | Compile user patterns; run once per repo in every org | yes | yes |
| `versions.Selected`, `versions.Behind` | Interpret version constraints and registry responses | yes | yes |
| `registry.ParseTags` | Parses `git ls-remote` output from remotes | yes | yes |
| `ghapi.Transport.RoundTrip` | Every GitHub and registry API call goes through it; it owns rate limit waits and backoff, parsing headers from the server | yes | yes |
| `discover.GitHub.HeadSHAs` | One GraphQL query per 100 repos replaces a git round trip per repo; repo names are embedded in the query | yes | yes |
| `pipeline.Run` | Orchestrates every clone and scan; the cached path is the common nightly case | yes | |
| `metrics.Compute`, `metrics.Hygiene` | The aggregation hot loop over every facts file | yes | |
| `sink.Rows`, OpenMetrics encoding | Run over every module, call and series on every emit | yes | |
| `app.LoadConfig` | Config resolution (flag > env > file) for every command, from a user-written YAML file | yes | yes |

## Commit style

Use [Conventional Commits](https://www.conventionalcommits.org/). The release changelog is grouped by type.

```text
feat(aggregate): add module_age_days metric
fix(scan): skip .terraform directories when walking a repo
docs: explain the markdown sink
perf(parse): reuse the HCL parser across files
```

Breaking changes add `!` after the type or a `BREAKING CHANGE:` footer.

## Pull requests

1. Fork and branch from `main`.
2. Keep the change focused, with tests, and run `make check`.
3. Open a PR whose title is a Conventional Commit; PRs are squash-merged, so the title becomes the commit message.
4. A maintainer from [CODEOWNERS](.github/CODEOWNERS) reviews, and all required CI checks must pass before merge.

## Questions

Open a [GitHub issue](https://github.com/rossedman/tfmetrics/issues) with the question or feature request template, or start from the docs.
