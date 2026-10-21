## tfmetrics scan

Parse repos into facts JSON

### Synopsis

Parse one or more repos into facts files, one per repo. Each argument is a
local directory, a git URL, or a GitHub owner/name (shallow-cloned with a
sparse checkout). Nothing is fetched beyond the clone.

With --sink, the facts are also aggregated and emitted, which is how the
per-repo PR check works: tfmetrics scan . --sink markdown

```
tfmetrics scan <path|repo>... [flags]
```

### Examples

```
  tfmetrics scan .
  tfmetrics scan ./infra acme/tf-network --out facts/
  tfmetrics scan . --sink markdown
```

### Options

```
  -h, --help           help for scan
      --out string     directory to write facts files to (default "facts")
      --ref string     branch, tag or commit to clone for remote repos (default: the default branch)
      --sink strings   also aggregate and emit to these sinks: json, openmetrics, otlp, csv, parquet, markdown
```

### Options inherited from parent commands

```
      --config string   config file (default: tfmetrics.yaml in $XDG_CONFIG_HOME/tfmetrics, $HOME or the current directory)
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing

