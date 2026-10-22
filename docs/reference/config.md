# Config file

Every key `tfmetrics.yaml` accepts, with its type, default and what overrides it. For what the settings do and when to change them, see [Configuration](../configuration.md).

## Finding the file

`--config <path>` names the file to read. It must exist; a missing file is an error. Its format comes from the extension: `.json` and `.toml` are read as JSON and TOML, and every other name (`.yaml`, `.yml`, `tfmetrics.conf`, a file with no extension, `/dev/stdin`) is read as YAML.

Without `--config`, tfmetrics reads the first `tfmetrics.yaml` or `tfmetrics.yml` it finds in these directories, in order, always as YAML:

1. `$XDG_CONFIG_HOME/tfmetrics/` (only when `XDG_CONFIG_HOME` is set)
2. `~/.config/tfmetrics/`
3. `~/`
4. The current directory

Only those two names are searched, so the `tfmetrics` binary in the current directory is never read as config. No file is fine: every key has a default.

## Precedence

For each key, the first of these that is set wins:

1. A flag given on the command line.
2. An environment variable: `TFMETRICS_` plus the key path in upper case, with each level and every dash joined by `_`. `scan.cache_dir` is `TFMETRICS_SCAN_CACHE_DIR`.
3. The config file.
4. The default.

!!! warning "Environment variables only reach some keys"
    An environment variable is read only for a key that the config file sets, or that a flag of the running command is bound to. `TFMETRICS_SCAN_CONCURRENCY=4 tfmetrics org` works because `org` has `--concurrency`; `TFMETRICS_OWNERS_PROPERTY=team` does nothing unless the file sets `owners.property`. To drive a key from the environment, put it in the file with any value.

List values in an environment variable are comma-separated: `TFMETRICS_SCAN_IGNORE_DIRS=.terraform,examples`. Maps (`modules.aliases`, `include.properties`, `hygiene.weights`) and `sinks` cannot be set from the environment, and a variable for a map key empties that map, so leave those unset.

These flags are bound to keys, so they also make the key's environment variable work for that command:

| Flag | Key | Commands |
| --- | --- | --- |
| `--out` | `out` | `scan`, `org`, `aggregate` |
| `--ref` | `scan.ref` | `scan`, `org` |
| `--concurrency` | `scan.concurrency` | `org` |
| `--cache-dir` | `scan.cache_dir` | `org` |

Some flags replace config for one run without being bound to a key, so they have no environment variable: `--org` replaces `source.github.orgs`, `--name`, `--topic` and `--property` replace each org's `include`, and `--sink` picks entries from `sinks`.

## Complete example

Every key, at its default where it has one. Values for keys without a default are illustrations.

```yaml title="tfmetrics.yaml"
source:
  github:
    token_env: GITHUB_TOKEN
    base_url: ""
    max_retries: 0
    clone_url: ""
    defaults:
      archived: false
      forks: false
    orgs:
      - name: acme
        include:
          names: ["tf-*", "re:^infra-.+$"]
          topics: [terraform]
          properties:
            team: [platform, networking]
        exclude:
          names: ["tf-sandbox-*"]

scan:
  concurrency: 16
  ignore_dirs: [.terraform, examples, test]
  cache_dir: ~/.cache/tfmetrics    # default: <user cache dir>/tfmetrics
  root_overrides: ["stacks/*"]
  ref: ""

modules:
  internal_registry: app.terraform.io
  registry_token_env: TFE_TOKEN
  aliases:
    "git::https://github.com/acme/tf-vpc.git": app.terraform.io/acme/vpc/aws

labels:
  properties: [team]
  drop: [resource_type]

owners:
  property: owner

hygiene:
  weights:
    pinned: 30
    required_version: 15
    provider_constraints: 15
    typed_vars: 15
    described: 15
    parse_clean: 10

sinks:
  - json:
      path: metrics/
  - openmetrics:
      path: metrics/metrics.prom
      pushgateway: ""
      job: tfmetrics
  - otlp:
      endpoint: localhost:4317
      insecure: false
      headers:
        x-api-key: example
  - csv:
      path: metrics/
  - parquet:
      path: metrics/
  - markdown:
      path: "-"
      step_summary: false
```

## Keys

An environment variable in the Override column only takes effect as the [warning above](#precedence) describes.

### source.github

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `token_env` | string | `GITHUB_TOKEN` | `TFMETRICS_SOURCE_GITHUB_TOKEN_ENV` | Name of the environment variable holding the GitHub token. |
| `base_url` | string | empty (github.com) | `TFMETRICS_SOURCE_GITHUB_BASE_URL` | GitHub Enterprise Server API URL. |
| `max_retries` | int | `0` (6 retries) | `TFMETRICS_SOURCE_GITHUB_MAX_RETRIES` | Retries of a throttled or failed API call; negative turns retries off. |
| `clone_url` | string | empty (`https://github.com`) | `TFMETRICS_SOURCE_GITHUB_CLONE_URL` | Base URL `scan owner/name` clones from. |
| `defaults.archived` | bool | `false` | `TFMETRICS_SOURCE_GITHUB_DEFAULTS_ARCHIVED` | Include archived repos. |
| `defaults.forks` | bool | `false` | `TFMETRICS_SOURCE_GITHUB_DEFAULTS_FORKS` | Include forks. |
| `orgs` | list of orgs | empty | `--org` | Orgs `tfmetrics org` scans. |
| `orgs[].name` | string | required | | Org login. An empty name is an error. |
| `orgs[].include` | [filter](#filter) | match all | `--name`, `--topic`, `--property` | Repos must match it. |
| `orgs[].exclude` | [filter](#filter) | exclude none | | Matching repos are skipped. |

#### Filter

| Key | Type | Description |
| --- | --- | --- |
| `names` | list of strings | Repo name globs, or regular expressions with a `re:` prefix. |
| `topics` | list of strings | Topics, compared case-insensitively. |
| `properties` | map of string to list of strings | Custom property name to allowed values. |

Kinds are ANDed and values within a kind are ORed; see [Filters](../configuration.md#filters).

### scan

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `concurrency` | int | `16` | `--concurrency`, `TFMETRICS_SCAN_CONCURRENCY` | Repos cloned and scanned at once. Zero or negative means the default. |
| `ignore_dirs` | list of strings | `[.terraform, examples, test]` | `TFMETRICS_SCAN_IGNORE_DIRS` | Directory patterns skipped. Setting it replaces the default list. |
| `cache_dir` | string | user cache dir + `/tfmetrics` | `--cache-dir`, `TFMETRICS_SCAN_CACHE_DIR` | Facts cache. A leading `~` is expanded. |
| `root_overrides` | list of strings | empty | `TFMETRICS_SCAN_ROOT_OVERRIDES` | Directory patterns always classified as root modules. |
| `ref` | string | empty (default branch) | `--ref`, `TFMETRICS_SCAN_REF` | Branch, tag or commit to scan. |

### modules

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `internal_registry` | string | empty | `TFMETRICS_MODULES_INTERNAL_REGISTRY` | Host of your private registry. |
| `registry_token_env` | string | empty | `TFMETRICS_MODULES_REGISTRY_TOKEN_ENV` | Name of the environment variable holding the registry token. |
| `aliases` | map of string to string | empty | | Module source, as written, to the address it is reported as. |

### labels

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `properties` | list of strings | empty | `TFMETRICS_LABELS_PROPERTIES` | Custom properties promoted to labels. |
| `drop` | list of strings | empty | `TFMETRICS_LABELS_DROP` | Labels removed from every series. |

### owners

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `property` | string | `owner` | `TFMETRICS_OWNERS_PROPERTY` | Custom property consulted for a repo's owner. |

### hygiene

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `weights` | map of check to number | the six [default weights](metrics.md#hygiene-score) | | Weight of each check. Keys are `pinned`, `required_version`, `provider_constraints`, `typed_vars`, `described` and `parse_clean`; an unknown key is an error, and a key left out gets weight 0. |

### sinks

A list where each item has exactly one key, the sink name, whose value is a map of that sink's options. `--sink` picks sinks by name and takes their options from the matching entry. With neither, `aggregate` writes `json`. See [Sinks](../sinks/index.md).

| Sink | Option | Type | Default | Description |
| --- | --- | --- | --- | --- |
| `json` | `path` | string | `--out` | Directory for `metrics.json` and the facts files. |
| `openmetrics` | `path` | string | `<out>/metrics.prom` | File in OpenMetrics text format. |
| `openmetrics` | `pushgateway` | string | empty | Pushgateway URL; when set, metrics are pushed instead of written. |
| `openmetrics` | `job` | string | `tfmetrics` | Job name for the push. |
| `otlp` | `endpoint` | string | `localhost:4317` | OTLP gRPC endpoint. |
| `otlp` | `insecure` | bool | `false` | Use plaintext gRPC. |
| `otlp` | `headers` | map of string to string | empty | gRPC headers. |
| `csv` | `path` | string | `--out` | Directory for `modules.csv` and `module_calls.csv`. |
| `parquet` | `path` | string | `--out` | Directory for `modules.parquet` and `module_calls.parquet`. |
| `markdown` | `path` | string | `-` (stdout) | File to write. |
| `markdown` | `step_summary` | bool | `false` | Also append to `$GITHUB_STEP_SUMMARY`. |

### out

| Key | Type | Default | Override | Description |
| --- | --- | --- | --- | --- |
| `out` | string | `facts` (`scan`, `org`), `metrics` (`aggregate`) | `--out`, `TFMETRICS_OUT` | Output directory. |

`out` is the one top-level key outside the sections above. It is read from the file too: a top-level `out: dir` sets the output directory of `scan`, `org` and `aggregate` alike. Because those commands default to different directories (`facts` and `metrics`), prefer `--out` or `TFMETRICS_OUT` on each command.
