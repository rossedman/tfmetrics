## tfmetrics explain

Print a human-readable report for each repo

### Synopsis

Print a per-repo report from facts files: module inventory, hygiene score
and the checks behind it, remote module calls and their pinning, and parse
diagnostics.

```
tfmetrics explain <facts file|dir>... [flags]
```

### Examples

```
  tfmetrics explain facts/acme__infra.json
  tfmetrics explain facts/ --format markdown
```

### Options

```
      --format string   output format: text or markdown (default "text")
  -h, --help            help for explain
```

### Options inherited from parent commands

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing

