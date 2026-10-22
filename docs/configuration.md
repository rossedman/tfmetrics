# Configuration

Every setting lives in one `tfmetrics.yaml`, and every key has a default, so the file is optional for local scans and only needed to name orgs, shape labels, tune the score or choose sinks.

## Where settings come from

Settings are merged in this order, highest first:

1. Command-line flags (`--out`, `--ref`, `--concurrency`, `--cache-dir`, `--sink`, `--org` and the filter flags).
2. Environment variables with the `TFMETRICS_` prefix. Dots and dashes become underscores: `--out` is `TFMETRICS_OUT`. A variable is only read for a key the file sets or a flag binds; see [Precedence](reference/config.md#precedence).
3. The config file.

The [config file reference](reference/config.md) lists every key with its type, default and override.

Without `--config`, tfmetrics looks for `tfmetrics.yaml` (or `tfmetrics.yml`) in `$XDG_CONFIG_HOME/tfmetrics/`, `~/.config/tfmetrics/`, `~/` and the current directory, in that order, and uses the first one it finds. Only those two names are searched, so the `tfmetrics` binary sitting in the current directory is never mistaken for config. A missing file is fine; a file you name with `--config` must exist. Its format comes from its extension (`.yaml`, `.yml`, `.json` or `.toml`); any other name, such as `tfmetrics.conf`, is read as YAML.

Secrets never go in the file. The file names an environment variable (`token_env`, `registry_token_env`) and tfmetrics reads the value from there.

## Full example

Every key below is shown at its default except where a value is needed to illustrate it. Copy only what you change.

```yaml title="tfmetrics.yaml"
source:
  github:
    token_env: GITHUB_TOKEN        # env var holding the token
    base_url: ""                   # GitHub Enterprise API URL; empty = github.com
    max_retries: 0                 # retries of a throttled or failed API call; 0 = 6, negative = none
    clone_url: ""                  # base for `scan owner/name`; empty = https://github.com
    defaults:
      archived: false              # include archived repos
      forks: false                 # include forks
    orgs:
      - name: acme
        include:                   # kinds are ANDed, values within a kind are ORed
          topics: [terraform]
          names: ["tf-*", "re:^infra-.+$"]
          properties:
            team: [platform, networking]
        exclude:                   # exclude always wins
          names: ["tf-sandbox-*"]
      - name: acme-labs            # no filters: every non-archived, non-fork repo

scan:
  concurrency: 16                  # repos cloned and scanned at once
  ignore_dirs: [.terraform, examples, test, tests, testdata]
  cache_dir: ~/.cache/tfmetrics    # default: <user cache dir>/tfmetrics
  root_overrides: ["stacks/*"]     # directories to treat as roots regardless
  ref: ""                          # branch, tag or commit; empty = default branch

modules:
  internal_registry: app.terraform.io
  registry_token_env: TFE_TOKEN
  aliases:
    "git::https://github.com/acme/tf-vpc.git": tfe.acme.internal/acme/vpc/aws

labels:
  properties: [team, tier]         # custom properties promoted to labels
  drop: [resource_type]            # labels removed from every series

owners:
  property: owner                  # custom property consulted for the owner

hygiene:
  weights:
    pinned: 30
    required_version: 15
    provider_constraints: 15
    typed_vars: 15
    described: 15
    parse_clean: 10

sinks:
  - openmetrics:
      path: metrics/metrics.prom
      job: tfmetrics
  - parquet:
      path: metrics/
```

## source.github

Where repos come from. `tfmetrics org` needs at least one org here or `--org` on the command line; `tfmetrics scan` on a local path needs none of this.

| Key | Default | Description |
| --- | --- | --- |
| `token_env` | `GITHUB_TOKEN` | Name of the environment variable that holds a GitHub token. Used for the API, for clones and for private git module tag lookups. Git gets it through `GIT_CONFIG_*` environment entries added after any you already set. |
| `base_url` | empty | API URL of a GitHub Enterprise Server, such as `https://ghe.acme.com/api/v3/`. Empty means github.com. |
| `max_retries` | `0` (6 retries) | How often a throttled or failed API call is retried. Waits follow GitHub's rate limit policy; see [Rate limits and speed](guides/nightly-org-scan.md#rate-limits-and-speed). A negative value turns retries off. |
| `clone_url` | `https://github.com` | Base URL that `tfmetrics scan owner/name` clones from. Org scans use the clone URL the API returns. |
| `defaults.archived` | `false` | Include archived repos. |
| `defaults.forks` | `false` | Include forks. |
| `orgs[].name` | required | The org login. |
| `orgs[].include` | all repos | A [filter](#filters) a repo must match. |
| `orgs[].exclude` | nothing | A [filter](#filters); a repo that matches it is skipped. |

### Filters

`include` and `exclude` take the same three kinds of filter. Within one filter every kind you set must match (AND), and within a kind any value may match (OR).

| Kind | Matches |
| --- | --- |
| `names` | The repo name, without the org. A glob (`tf-*`), or a regular expression with a `re:` prefix (`re:^infra-.+$`). |
| `topics` | Any listed topic, compared case-insensitively. |
| `properties` | A map of GitHub [custom property](https://docs.github.com/en/organizations/managing-organization-settings/managing-custom-properties-for-repositories-in-your-organization) name to the allowed values. A repo with no value for a listed key does not match. Multi-select values are joined with commas in facts and labels. |

An empty `include` matches everything, and an empty `exclude` excludes nothing. Archived repos and forks are dropped before either filter runs unless `defaults` says otherwise. On the command line, `--name`, `--topic` and `--property` replace the `include` filter of every org for one run; use `--dry-run` to see what a filter selects before cloning anything.

## scan

| Key | Default | Description |
| --- | --- | --- |
| `concurrency` | `16` | Repos cloned and scanned at once. |
| `ignore_dirs` | `[.terraform, examples, test, tests, testdata]` | Directories skipped with everything below them: examples, test fixtures and test data rather than infrastructure. A pattern without a slash matches a directory's name at any depth; one with a slash matches the path from the repo root. Patterns use Go's `path.Match` syntax. Hidden directories are always skipped. Setting this replaces the default list. |
| `cache_dir` | user cache dir + `/tfmetrics` | Where facts are cached by commit SHA. A leading `~` is expanded. |
| `root_overrides` | empty | Directory patterns (`stacks/*`) always classified as root modules, for roots with no backend, `cloud` block or configured provider. |
| `ref` | default branch | Branch, tag or commit to scan in every repo. |

A cached entry is keyed by the commit plus a hash of the repo's topics, custom properties and scan settings, so changing `ignore_dirs`, `root_overrides`, the registry settings or `owners.property` rescans automatically. `--no-cache` ignores the cache for one run.

## modules

How module sources are normalized and where versions are looked up. See [versions_behind](reference/metrics.md#tf_module_versions_behind).

| Key | Default | Description |
| --- | --- | --- |
| `internal_registry` | empty | Host of your private registry, such as `app.terraform.io`. It is the host that `registry_token_env`'s token is sent to. Any registry host other than `registry.terraform.io` and `registry.opentofu.org` is already classified `private_registry`. |
| `registry_token_env` | empty | Environment variable holding a bearer token for `internal_registry`. Without it, version lookups against a private registry that needs auth fail with a warning and that module gets no `tf_module_versions_behind` series. |
| `aliases` | empty | Map of a module source, as written, to the address it should be reported as. Use it to merge two spellings of one module, or a mirror and its upstream. |

## labels

Every series carries `org`, `repo` and `owner` where it makes sense; these keys change the rest.

| Key | Default | Description |
| --- | --- | --- |
| `properties` | empty | Custom property names promoted to labels on every series, so you can group by `team` or `tier`. A repo without the property gets an empty label value. |
| `drop` | empty | Label names removed from every series. Series that become identical are summed, and `tf_hygiene_score` series are averaged. Use it to cap cardinality, for example `[resource_type]`, or `[repo, owner]` for an org-level rollup. |

## owners

| Key | Default | Description |
| --- | --- | --- |
| `property` | `owner` | Custom property consulted for a repo's owner. |

The `owner` label comes from the first source that has an answer: the catch-all (`*`) rule of the repo's `CODEOWNERS` (or the owners of every rule when there is no catch-all), then this custom property, then topics named `team-<name>`, else `unowned`.

## hygiene

| Key | Default | Description |
| --- | --- | --- |
| `weights` | see [hygiene score](reference/metrics.md#hygiene-score) | Weight of each of the six checks. |

Weights are relative: the score divides by their sum, so they do not need to add to 100. List all six keys. A key you leave out gets weight 0, and an unknown key is an error.

## sinks

A list where each item has exactly one key, the sink name, whose value is that sink's options. `--sink` on the command line picks sinks by name and takes their options from the matching entry here. With neither, `aggregate` writes the `json` sink. Each sink has a [tutorial](sinks/index.md) that walks through setting it up.

| Sink | Option | Default | Description |
| --- | --- | --- | --- |
| `json` | `path` | `--out` | Directory for `metrics.json` and a copy of every facts file. |
| `openmetrics` | `path` | `<out>/metrics.prom` | File to write in OpenMetrics text format. |
| `openmetrics` | `pushgateway` | empty | A Pushgateway URL. When set, the metrics are pushed instead of written to a file. |
| `openmetrics` | `job` | `tfmetrics` | Job name for the push. |
| `otlp` | `endpoint` | `localhost:4317` | OTLP gRPC endpoint. |
| `otlp` | `insecure` | `false` | Use plaintext gRPC. |
| `otlp` | `headers` | empty | Map of gRPC headers, for example an API key. |
| `csv` | `path` | `--out` | Directory for `modules.csv` and `module_calls.csv`. |
| `parquet` | `path` | `--out` | Directory for `modules.parquet` and `module_calls.parquet`. |
| `markdown` | `path` | `-` | File to write, or `-` for stdout. |
| `markdown` | `step_summary` | `false` | Also append to the file named by `$GITHUB_STEP_SUMMARY`. |

File sinks write to local paths only. For object storage, write locally and upload (`aws s3 cp`, `gcloud storage cp`).
