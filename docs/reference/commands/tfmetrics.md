## tfmetrics

Org-wide Terraform metrics from static parsing

### Synopsis

tfmetrics statically parses every Terraform repo in an org and emits one
consistent set of metrics: inventory, version drift, module adoption and
hygiene. It never runs terraform init or plan, and never touches state or
cloud credentials.

Config precedence: flag > environment (TFMETRICS_*) > config file.

### Options

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
  -h, --help            help for tfmetrics
```

### SEE ALSO

* [tfmetrics aggregate](tfmetrics_aggregate.md)	 - Compute the metrics catalog from facts files and write it to sinks
* [tfmetrics explain](tfmetrics_explain.md)	 - Print a human-readable report for each repo
* [tfmetrics org](tfmetrics_org.md)	 - Discover, clone and scan every matching repo in one or more orgs
* [tfmetrics scan](tfmetrics_scan.md)	 - Parse repos into facts JSON
* [tfmetrics version](tfmetrics_version.md)	 - Print the version, commit and build date


Every key the config file accepts, and which flag or environment variable overrides it, is in the [config file reference](../config.md).
