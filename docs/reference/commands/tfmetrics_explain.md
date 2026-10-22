## tfmetrics explain

Print a human-readable report for each repo

### Synopsis

Print a per-repo report from facts files: module inventory, hygiene score
and the checks behind it, remote module calls and their pinning, and parse
diagnostics.

For each pinned remote module call, the report shows the latest release and
how many releases newer than the version the call uses there are, as major,
minor and patch releases. That needs registry and git tag lookups; --offline
skips them.

```
tfmetrics explain <facts file|dir>... [flags]
```

### Examples

```
  tfmetrics explain facts/acme__infra.json
  tfmetrics explain facts/ --format markdown
  tfmetrics explain facts/ --offline
```

### Options

```
      --format string   output format: text or markdown (default "text")
  -h, --help            help for explain
      --offline         skip registry and git tag lookups (no latest or behind columns)
```

### Options inherited from parent commands

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
      --plain           print plain lines: no colors, styling or live progress, as when output is not a terminal
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing


Every key the config file accepts, and which flag or environment variable overrides it, is in the [config file reference](../config.md).
