# CLAUDE.md

tfmetrics is a Go CLI (cobra + viper) that statically parses Terraform repos into facts JSON and computes org-wide metrics. The product spec is "tfmetrics — Org-wide Terraform Metrics Spec"; the docs site under `docs/` describes current behavior.

This repo uses the `rossedman` plugin marketplace (see `.claude/settings.json`): `go-bootstrap` sets the standards below and runs gofmt, lint and tests as hooks; `oss-docs` blocks a push when the docs fail a strict build.

## Layout

- `cmd/`: one cobra command per file (`tools/check-cmds.sh` enforces it). Commands stay thin; wiring lives in `internal/app`.
- `internal/`: the pipeline stages: `discover` → `clone` → `classify` + `parse` (via `scan`) → `facts` → `metrics` → `sink`. `pipeline` runs scans in parallel with the `cache`; `source` normalizes module sources; `versions` + `registry` compute drift; `report` renders `explain` and the markdown sink.
- `internal/facts` is the contract between scanning and aggregation. Changing a field's meaning or removing one bumps `facts.SchemaVersion`.
- `testdata/repos/acme-infra`: the fixture repo. Its counts are hand-checked; tests depend on them.

## Rules

- Never evaluate Terraform, run `terraform`, download modules, or read state. Parse problems become `Diagnostic` facts, never errors.
- Every metric is a `tf_` gauge labelled with `repo`, `owner` and `org` unless the spec says otherwise; add new metrics to `internal/metrics` (name, `Help`) and to `docs/reference/metrics.md`.
- Every function and method has a doc comment (`make doccheck`); every package has a package comment.
- Hand-written code stays at or above 90% coverage (`make cover`). Critical paths (listed in `CONTRIBUTING.md`) need a benchmark, and input-handling ones a fuzz target.
- Tests never call the real GitHub API or registries: use httptest servers and local `file://` git repos.
- After changing a command, run `make docs` to regenerate `docs/reference/commands/`.
- Run `make check` before pushing. Commits follow Conventional Commits.
