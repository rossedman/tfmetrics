## tfmetrics who-uses

List every call to a module, or use of a provider, across facts files

### Synopsis

List every call to a module across facts files: which repo and module makes
it, where the module block is, the version it asks for, how many release
lines that version is behind, and who owns the repo. With --provider, list
every required_providers entry for a provider instead.

The module or provider can be written whole or by its trailing segments:
terraform-aws-modules/eks/aws and eks/aws both match
registry.terraform.io/terraform-aws-modules/eks/aws, including calls to its
//subdirectories. A provider is matched by its source, such as hashicorp/aws.

How far behind each use is needs registry and git tag lookups; --offline skips
them. --behind keeps only the uses at least that far behind, which makes the
list an upgrade campaign: every caller still to move, and its owner.

```
tfmetrics who-uses <module|provider> <facts dir|file>... [flags]
```

### Examples

```
  tfmetrics who-uses terraform-aws-modules/eks/aws facts/
  tfmetrics who-uses eks/aws facts/ --behind 2
  tfmetrics who-uses hashicorp/aws facts/ --provider --format csv > aws.csv
```

### Options

```
      --behind int      only list uses at least this many release lines behind the latest
      --format string   output format: text, json or csv (default "text")
  -h, --help            help for who-uses
      --offline         skip registry and git tag lookups (no behind counts)
      --provider        search provider requirements instead of module calls
```

### Options inherited from parent commands

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
      --plain           print plain lines: no colors, styling or live progress, as when output is not a terminal
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing


Every key the config file accepts, and which flag or environment variable overrides it, is in the [config file reference](../config.md).
