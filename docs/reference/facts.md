# Facts file

A facts file is the complete, structured record of one repo at one commit. It is the contract between scanning and aggregation: `scan` and `org` write it, and `aggregate`, `explain` and every sink read it. Nothing downstream looks at your Terraform again.

Facts are plain JSON, one file per repo, named `<org>__<repo>.json` (`acme/infra` becomes `acme__infra.json`). Keep them as build artifacts, diff them between runs, or load them into anything that reads JSON.

## Compatibility

`schema_version` is `1`. A build reads files from schema 1 up to the version it writes and rejects anything newer with a clear error, so an older `aggregate` never silently misreads facts from a newer `scan`.

`aggregate` and `explain` take files or directories. A directory contributes every `*.json` file directly inside it (not subdirectories), except `metrics.json`.

## Repo

| Field | Type | Description |
| --- | --- | --- |
| `schema_version` | number | Facts format version. |
| `repo` | string | `org/name`. For a local scan, from the checkout's `origin` remote, or `local/<dir>`. |
| `org` | string | The org part of `repo`. |
| `topics` | string list | GitHub topics at scan time. Empty for local scans. |
| `properties` | map | GitHub custom properties at scan time, multi-select values joined with commas. Empty for local scans. |
| `commit` | string | The commit that was scanned. This is the cache key. |
| `scanned_at` | timestamp | UTC, to the second. |
| `owners` | string list | Resolved owners, or `["unowned"]`. See [`owners`](../configuration.md#owners). |
| `modules` | list | One entry per module directory. |

## Module

A module is a directory that holds `.tf` or `.tf.json` files. Directories named in [`scan.ignore_dirs`](../configuration.md#scan) and hidden directories are skipped. `.tofu` files are counted in `files` and `lines` but not parsed, and add a warning diagnostic.

| Field | Type | Description |
| --- | --- | --- |
| `path` | string | Directory relative to the repo root, `.` for the root itself. |
| `kind` | string | `root` or `child`. A directory is a root when it has a `backend` block, a `cloud` block, a `provider` block that sets anything beyond `alias` and `version`, or matches [`scan.root_overrides`](../configuration.md#scan). Otherwise it is a child. |
| `files`, `lines` | number | Terraform files and lines in the directory. |
| `required_version` | string | The `required_version` constraint, or empty. |
| `backend` | string | `s3`, `gcs`, `remote` and so on; `cloud` for a `cloud` block; empty if none. |
| `providers` | list | `name`, `source` (empty if not declared) and `constraint` (empty if none). Includes providers that are used or configured but not in `required_providers`. |
| `resources`, `data_sources` | list | `type`, `name`, `provider`, and `count`, `for_each`, `prevent_destroy` when set. |
| `module_calls` | list | See below. |
| `variables` | list | `name`, `has_type`, `has_description`, `sensitive`, `has_default`. |
| `outputs` | list | `name`, `has_description`, `sensitive`. |
| `lifecycle` | object | Counts of `prevent_destroy`, `ignore_changes` and `create_before_destroy` across the module's resources. `prevent_destroy` and `create_before_destroy` count only the literal `true`. |
| `moved_blocks`, `import_blocks` | number | Counts of `moved` and `import` blocks. |
| `diagnostics` | list | Parse errors and warnings. See below. |

## Module call

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | The module block label. |
| `source` | string | The source exactly as written. |
| `source_kind` | string | `local`, `registry`, `private_registry`, `git`, `http`, `s3`, `gcs` or `unknown`. |
| `address` | string | The normalized address, shared by every spelling of the same module. For local calls, the path from the repo root. |
| `version` | string | The version constraint for registry sources, the `?ref=` for git sources, or empty. |
| `pinned` | string | `exact`, `range` or `none`; empty for local calls. |
| `count`, `for_each` | boolean | Present and true when the block uses them. |

How `pinned` is decided:

| Source | `exact` | `range` | `none` |
| --- | --- | --- | --- |
| Registry | One full version, `1.2.3` or `= 1.2.3` | Anything else, such as `~> 3.15`, `>= 1.0`, `>= 1, < 2` or `1.2` | No `version` |
| Git | A semver tag or a commit SHA as `ref` | Any other ref, such as a branch name | No `ref` |
| `http`, `s3`, `gcs` | A `ref`, or a version in the path | | Neither |

## Diagnostics

Parse problems are facts, never failures. Each diagnostic has `severity` (`error` or `warning`), `summary`, an optional `detail`, and the `file` and `line` when known. Files with errors drive `tf_parse_errors` and the `parse_clean` check.

## Example

One module from the example repo, as written to `facts/acme__infra.json`:

```json
{
  "path": "live/staging",
  "kind": "root",
  "files": 1,
  "lines": 28,
  "required_version": "",
  "backend": "cloud",
  "providers": [
    { "name": "aws", "source": "hashicorp/aws", "constraint": "" }
  ],
  "resources": [
    { "type": "aws_sqs_queue", "name": "jobs", "provider": "aws" }
  ],
  "data_sources": [],
  "module_calls": [
    {
      "name": "eks",
      "source": "terraform-aws-modules/eks/aws",
      "source_kind": "registry",
      "address": "registry.terraform.io/terraform-aws-modules/eks/aws",
      "version": "17.24.0",
      "pinned": "exact"
    },
    {
      "name": "vpc",
      "source": "../../modules/vpc",
      "source_kind": "local",
      "address": "modules/vpc",
      "version": "",
      "pinned": ""
    }
  ],
  "variables": [],
  "outputs": [],
  "lifecycle": { "prevent_destroy": 0, "ignore_changes": 0, "create_before_destroy": 0 },
  "moved_blocks": 0,
  "import_blocks": 0,
  "diagnostics": []
}
```

The file is written with each object expanded over several lines; it is condensed here to fit.
