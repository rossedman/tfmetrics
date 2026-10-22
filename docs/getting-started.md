# Install and first run

Install the binary, scan one repo on disk, read the report, and write a metrics file. It takes about two minutes and needs no GitHub token, no cloud credentials and no `terraform` binary.

## Install

=== "Homebrew"

    ```sh
    brew install rossedman/tap/tfmetrics
    tfmetrics version
    ```

    Works on macOS and Linux. `brew upgrade tfmetrics` picks up new releases.

=== "go install"

    ```sh
    go install github.com/rossedman/tfmetrics@latest
    tfmetrics version
    ```

    Needs the Go version in `go.mod`. The binary lands in `$(go env GOPATH)/bin`.

=== "Release download"

    Pick the archive for your platform from the [releases page](https://github.com/rossedman/tfmetrics/releases). Linux, macOS and Windows builds exist for amd64 and arm64.

    ```sh
    VERSION=0.1.0        # the release without its leading v
    OS=linux ARCH=amd64  # linux|darwin|windows and amd64|arm64
    BASE=https://github.com/rossedman/tfmetrics/releases/download/v${VERSION}

    curl -fsSLO ${BASE}/tfmetrics_${VERSION}_${OS}_${ARCH}.tar.gz
    curl -fsSLO ${BASE}/checksums.txt
    curl -fsSLO ${BASE}/checksums.txt.sigstore.json
    ```

    Verify the download before you run it (below), then unpack:

    ```sh
    tar -xzf tfmetrics_${VERSION}_${OS}_${ARCH}.tar.gz tfmetrics
    sudo install tfmetrics /usr/local/bin/
    ```

=== "From source"

    ```sh
    git clone https://github.com/rossedman/tfmetrics.git
    cd tfmetrics
    make build
    ./bin/tfmetrics version
    ```

### Verify a release

Every release is built by GitHub Actions. `checksums.txt` covers every archive and is signed keylessly with [cosign](https://docs.sigstore.dev/cosign/signing/overview/) through the workflow's GitHub identity, so there is no key to trust or rotate.

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rossedman/tfmetrics/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify tfmetrics_${VERSION}_${OS}_${ARCH}.tar.gz --repo rossedman/tfmetrics
```

A signature from any other repository or workflow fails the identity check. On macOS use `shasum -a 256 --ignore-missing -c checksums.txt`.

## Scan a repo

Point `scan` at any directory that holds Terraform. It writes one facts file per repo, and names the repo from the checkout's `origin` remote (or `local/<dir>` without one).

```console
$ tfmetrics scan ./infra
scanned acme/infra: 5 modules -> facts/acme__infra.json
```

Nothing was fetched and nothing was executed. `scan` also takes a git URL or a GitHub `owner/name`, which it shallow-clones with a sparse checkout of the Terraform files.

## Read the report

`explain` turns a facts file into a report: inventory, the hygiene score with the checks behind it, every remote module call and how it is pinned, and parse diagnostics.

```console
$ tfmetrics explain facts/acme__infra.json
acme/infra @ 1c35bc0af618
owners: acme/platform

modules      5 (2 root, 3 child)
resources    8 declared, 1 data sources
module calls 8 (1 remote unpinned)
lines        194
parse errors 1

hygiene score 65.3 / 100
  Remote module calls pinned           5/6  weight 30
  Variables and outputs described      4/8  weight 15
  Providers with a version constraint  2/5  weight 15
  Roots with required_version          1/2  weight 15
  Variables with a type                3/4  weight 15
  Modules that parse cleanly           4/5  weight 10

modules
  PATH            KIND   RESOURCES  CALLS  BACKEND  REQUIRED_VERSION
  broken          child  1          0      -        -
  live/prod       root   3          6      s3       >= 1.6.0
  live/staging    root   1          2      cloud    -
  modules/legacy  child  1          0      -        -
  modules/vpc     child  2          0      -        >= 1.5

remote module calls
  ADDRESS                                                    KIND              VERSION  PINNED  IN
  app.terraform.io/acme/sqs/aws                              private_registry  2.1.0    exact   live/prod
  github.com/acme/tf-dns                                     git               v1.4.0   exact   live/prod
  github.com/acme/tf-iam                                     git               -        none    live/prod
  registry.terraform.io/terraform-aws-modules/eks/aws        registry          19.21.0  exact   live/prod
  registry.terraform.io/terraform-aws-modules/eks/aws        registry          17.24.0  exact   live/staging
  registry.terraform.io/terraform-aws-modules/s3-bucket/aws  registry          ~> 3.15  range   live/prod

diagnostics
  error broken/main.tf:1: Unclosed configuration block
```

The broken file did not stop the scan. It became a diagnostic, a `parse errors` count and a lower `parse_clean` score.

## Write metrics

`aggregate` reads facts files, computes the [metrics catalog](reference/metrics.md) and writes it to one or more [sinks](guides/dashboards.md). `--offline` skips the registry and git tag lookups that `tf_module_versions_behind` needs.

```console
$ tfmetrics aggregate facts/ --offline --sink openmetrics
aggregated 1 repos (1 with Terraform) into 36 series
$ grep hygiene metrics/metrics.prom | tail -1
tf_hygiene_score{org="acme",owner="acme/platform",repo="acme/infra"} 65.3
```

Drop `--offline` to look up each module's available versions and add `tf_module_versions_behind`. Lookups that fail (a private registry without a token, an unreachable git remote) print a warning and leave that series out; they never fail the run.

## Next steps

- Scan a whole org: set `source.github.orgs` in [the config](configuration.md) and run `tfmetrics org`, or follow [Nightly org scan](guides/nightly-org-scan.md).
- Gate pull requests on one repo: [PR check](guides/pr-check.md).
- Understand the stages: [How it works](how-it-works.md).
