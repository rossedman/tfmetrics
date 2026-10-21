# Security policy

## Supported versions

tfmetrics is pre-1.0 in spirit and fixes security issues on the latest release only. Upgrade to the newest version from the [releases page](https://github.com/rossedman/tfmetrics/releases) before reporting, if you can.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Older releases | No |

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately with GitHub private vulnerability reporting: <https://github.com/rossedman/tfmetrics/security/advisories/new>. This is the only channel; it keeps the report visible to maintainers alone until a fix ships.

Include the tfmetrics version (`tfmetrics version`), what you did, what happened, and the impact you see. A minimal Terraform repo or facts file that reproduces the problem helps most.

## What to expect

- Acknowledgement within 3 business days.
- A fix or a written plan within 30 days.
- Credit in the published security advisory, unless you prefer to stay anonymous.

## Scope

tfmetrics parses Terraform source statically. It never runs `terraform init` or `plan`, and never reads state or cloud credentials. In scope: crashes or resource exhaustion on crafted input, unsafe handling of cloned repositories, leaking the GitHub token used by `tfmetrics org`, and flaws in how release artifacts are built or signed.

## Verifying releases

Every release is built by the [`release.yml`](.github/workflows/release.yml) workflow. The `checksums.txt` file covers every archive and is signed keylessly with cosign through GitHub OIDC; the signature, certificate and transparency log proof are in `checksums.txt.sigstore.json`. Build provenance attestations cover each archive and `checksums.txt`.

Download an archive, `checksums.txt` and `checksums.txt.sigstore.json` from the release, then verify (replace the version in the archive name with the one you downloaded):

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rossedman/tfmetrics/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify tfmetrics_*_linux_amd64.tar.gz --repo rossedman/tfmetrics
```

A signature from any other repository or workflow fails the identity check. On macOS, use `shasum -a 256 --ignore-missing -c checksums.txt`.
