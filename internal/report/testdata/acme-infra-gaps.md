### tfmetrics: acme/infra

**Hygiene score: 65.6 / 100** · 6 modules (2 root, 4 child) · 8 resources · 9 module calls (1 unpinned) · 3 outdated (2 major, 0 minor, 1 patch) · 1 parse errors

| Check | Passed | Weight |
| --- | --- | --- |
| Remote module calls pinned | 5/6 | 30 |
| Variables and outputs described | 4/8 | 15 |
| Providers with a version constraint | 2/5 | 15 |
| Roots with required_version | 1/2 | 15 |
| Variables with a type | 3/4 | 15 |
| Modules that parse cleanly | 5/6 | 10 |

<details><summary>Remote module calls</summary>

| Address | Version | Pinned | Latest | Behind | In |
| --- | --- | --- | --- | --- | --- |
| `app.terraform.io/acme/sqs/aws` | `2.1.0` | exact | — | — | `live/prod` |
| `github.com/acme/tf-dns` | `v1.4.0` | exact | `1.4.1` | 1 patch | `live/prod` |
| `github.com/acme/tf-iam` | — | none | — | — | `live/prod` |
| `registry.terraform.io/terraform-aws-modules/eks/aws` | `19.21.0` | exact | `20.1.0` | 3 (1 major, 1 minor, 1 patch) | `live/prod` |
| `registry.terraform.io/terraform-aws-modules/eks/aws` | `17.24.0` | exact | `20.1.0` | 6 (3 major, 1 minor, 2 patch) | `live/staging` |
| `registry.terraform.io/terraform-aws-modules/s3-bucket/aws` | `~> 3.15` | range | `3.16.0` | up to date | `live/prod` |

</details>

<details><summary>Diagnostics</summary>

- **error** `broken/main.tf:1`: Unclosed configuration block

</details>

