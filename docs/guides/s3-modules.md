# Modules in S3

Many orgs publish their modules as archives in an S3 bucket and call them by URL:

```hcl
module "vpc" {
  source = "s3::https://s3-eu-west-1.amazonaws.com/acme-tf-modules/vpc/v1.2.0/vpc.zip"
}
```

tfmetrics reads the version from the path, groups every version of a module under one address, and works out how far behind each call is in one of two ways: by listing the bucket, or by looking at the git repo or registry the archives are built from.

## How the source is read

The last version-shaped part of the object key is the version, written as a directory or in the file name. In the address, it is replaced by `*`:

| Source | Address | Version |
| --- | --- | --- |
| `s3::https://s3-eu-west-1.amazonaws.com/acme-tf/vpc/v1.2.0/vpc.zip` | `s3-eu-west-1.amazonaws.com/acme-tf/vpc/*/vpc.zip` | `v1.2.0` |
| `https://acme-tf.s3.amazonaws.com/modules/vpc-1.4.0.zip` | `acme-tf.s3.amazonaws.com/modules/vpc-*.zip` | `1.4.0` |
| `s3::https://s3.amazonaws.com/acme-tf/vpc_v2.0.0.tar.gz//modules/endpoints` | `s3.amazonaws.com/acme-tf/vpc_*.tar.gz//modules/endpoints` | `v2.0.0` |
| `s3::https://s3.amazonaws.com/acme-tf/vpc.zip?version=3HL4kqtJ` | `s3.amazonaws.com/acme-tf/vpc.zip` | `3HL4kqtJ` (an S3 object version) |
| `s3::https://s3.amazonaws.com/acme-tf/vpc.zip` | `s3.amazonaws.com/acme-tf/vpc.zip` | none |

A version in the path or an object version counts as `exact` pinning. A call with neither always gets whatever object is at that key today, so it counts as unpinned. Only a version in the path can be compared with other releases: an object version ID says nothing about which release it is.

Every call to the module now shares one address, so [`who-uses`](who-uses.md), the metrics and the reports see one module with several versions, not one module per version:

```console
$ tfmetrics who-uses acme-tf/vpc facts/
```

`gcs::` and plain `https://` archives are read the same way, but only S3 is listed for versions. For those, use an alias to the git repo (option 2).

## Option 1: list the bucket

With AWS credentials, tfmetrics lists the objects under the module's prefix and reads the versions from their keys. For `vpc/*/vpc.zip` it lists `vpc/` and keeps the keys shaped like `vpc/<version>/vpc.zip`. It lists keys only and never downloads an object.

Credentials come from the standard AWS chain, as for the AWS CLI: `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`, `AWS_PROFILE` (including SSO profiles), web identity (such as GitHub Actions OIDC) or an instance role. The region comes from the address when it names one (`s3-eu-west-1.amazonaws.com`, `acme-tf.s3.eu-west-1.amazonaws.com`), and is otherwise looked up from the bucket.

The role needs one permission on each module bucket:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "s3:ListBucket",
    "Resource": "arn:aws:s3:::acme-tf-modules"
  }]
}
```

In the [nightly scan](nightly-org-scan.md), give the job an OIDC role before `aggregate`:

```yaml
    permissions:
      contents: read
      id-token: write   # for the AWS role
    steps:
      # ...
      - uses: aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd # v6.3.0
        with:
          role-to-assume: arn:aws:iam::123456789012:role/tfmetrics-read-modules
          aws-region: eu-west-1
      - name: Aggregate and publish
        run: tfmetrics aggregate facts/
```

S3-compatible servers work too. A forced `s3::` source on a host outside `amazonaws.com`, such as `s3::https://minio.acme.io/tf-modules/vpc/v1.2.0/vpc.zip`, is listed at that host over HTTPS, with the bucket as the first part of the path.

A lookup that fails, for example with no credentials or no `s3:ListBucket`, prints a warning and leaves that module's drift out, as for any other lookup.

## Option 2: point at the source repo

If the archives are built from a git repo whose release tags match the versions in the bucket, or published to a registry too, an [alias](../configuration.md#modules) sends tfmetrics there instead. No AWS access is needed:

```yaml title="tfmetrics.yaml"
modules:
  aliases:
    "s3::https://s3-eu-west-1.amazonaws.com/acme-tf/vpc/*/vpc.zip": github.com/acme/terraform-aws-vpc
    "s3::https://s3-eu-west-1.amazonaws.com/acme-tf/eks/*/eks.zip": app.terraform.io/acme/eks/aws
```

Write the source with `*` for the version, or with any one version: both name every version of the module. The calls are then reported as calls to the alias target, with its kind (`git` or `private_registry`) and their own version, so `v1.2.0` from the bucket is compared with the repo's tags. That also merges them with any calls to the repo or registry directly.

Changing aliases rescans every repo on the next run, because the cache key includes them.

## Which to choose

- **List the bucket** when the bucket is the source of truth, or the archives do not map one to one onto a repo's tags.
- **Alias to the repo** when every archive is a tagged release of one repo, or when you would rather report the module by its repo name everywhere.
