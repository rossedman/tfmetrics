## tfmetrics org

Discover, clone and scan every matching repo in one or more orgs

### Synopsis

Discover repos in one or more GitHub orgs, apply each org's name, topic and
custom property filters, then shallow-clone and scan the matches in parallel.
A repo whose HEAD matches the cache reuses its last facts without cloning.

Orgs and filters come from source.github.orgs in the config. --org replaces
that list for a one-off run; filter flags replace the include filters of
every org named. --local-dir scans a directory of existing clones instead of
calling GitHub.

```
tfmetrics org [flags]
```

### Examples

```
  tfmetrics org --out facts/
  tfmetrics org --org acme --org acme-labs --out facts/
  tfmetrics org --org acme --topic terraform --property team=platform,networking --name 'tf-*' --dry-run
  tfmetrics org --org acme --local-dir ./clones --out facts/
```

### Options

```
      --cache-dir string       facts cache keyed by repo and commit (default: the user cache dir)
      --concurrency int        repos to clone and scan at once (default 16)
      --dry-run                print the matched repos without cloning
  -h, --help                   help for org
      --json                   with --dry-run, print the matched repos as JSON
      --local-dir string       scan the git checkouts in this directory instead of calling GitHub
      --name strings           include repos whose name matches a glob, or a regex with a re: prefix
      --no-cache               ignore the cache and rescan every repo
      --org strings            GitHub org to scan; repeat for several (replaces source.github.orgs)
      --out string             directory to write facts files to (default "facts")
      --property stringArray   include repos whose custom property matches: key=value[,value]; repeat for more keys
      --ref string             branch, tag or commit to scan (default: each repo's default branch)
      --sink strings           also aggregate and emit to these sinks: json, openmetrics, otlp, csv, parquet, markdown
      --strict                 exit non-zero if any repo fails to scan
      --topic strings          include repos with any of these topics
```

### Options inherited from parent commands

```
      --config string   config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)
```

### SEE ALSO

* [tfmetrics](tfmetrics.md)	 - Org-wide Terraform metrics from static parsing


Every key the config file accepts, and which flag or environment variable overrides it, is in the [config file reference](../config.md).
