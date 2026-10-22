## tfmetrics aggregate

Compute the metrics catalog from facts files and write it to sinks

### Synopsis

Read facts files, compute the metrics catalog and write it to every sink.
Sinks come from --sink, or from the sinks list in the config when no --sink
is given; with neither, the json sink is used. A --sink named in the config
takes its options from there.

Version drift (tf_module_versions_behind) needs registry and git tag lookups;
--offline skips them.

```
tfmetrics aggregate <facts dir|file>... [flags]
```

### Examples

```
  tfmetrics aggregate facts/ --sink openmetrics --sink parquet
  tfmetrics aggregate facts/ --sink json --out metrics/ --offline
```

### Options

```
  -h, --help           help for aggregate
      --offline        skip registry and git tag lookups (no versions_behind)
      --out string     default directory for file sinks (default "metrics")
      --sink strings   sinks to write: json, openmetrics, otlp, csv, parquet, markdown
```

### Options inherited from parent commands

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing

