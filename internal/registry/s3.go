package registry

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rossedman/tfmetrics/internal/source"
)

// S3Bucket is a bucket to list.
type S3Bucket struct {
	Name string
	// Region is the bucket's region; empty when the address does not say,
	// and the lister finds it.
	Region string
	// Endpoint is the base URL of an S3-compatible server such as MinIO;
	// empty for AWS.
	Endpoint string
}

// S3Lister lists the keys of the objects in a bucket under a prefix.
type S3Lister func(ctx context.Context, b S3Bucket, prefix string) ([]string, error)

// maxS3Keys caps how many keys one lookup lists, so a prefix that matches a
// whole bucket fails instead of paging through it.
const maxS3Keys = 100_000

// ParseS3 splits the address of a module in S3, such as
// acme-tf.s3.eu-west-1.amazonaws.com/vpc/*/vpc.zip, into its bucket and key.
// Both virtual-hosted and path-style addresses work, and so do S3-compatible
// servers outside amazonaws.com, which are reached over HTTPS path-style.
func ParseS3(address string) (S3Bucket, string, error) {
	address, _, _ = strings.Cut(address, "//")
	host, key, _ := strings.Cut(address, "/")
	h := strings.ToLower(host)
	var b S3Bucket
	switch {
	case !strings.HasSuffix(h, ".amazonaws.com"):
		b.Endpoint = "https://" + host
		b.Name, key, _ = strings.Cut(key, "/")
	case source.S3PathStyle(h):
		b.Region = s3Region(strings.TrimPrefix(h, "s3"))
		b.Name, key, _ = strings.Cut(key, "/")
	default:
		i := strings.LastIndex(h, ".s3")
		if i < 0 {
			return S3Bucket{}, "", fmt.Errorf("s3 address %q: %s is not an S3 host", address, host)
		}
		b.Name, b.Region = host[:i], s3Region(h[i+3:])
	}
	if b.Name == "" || key == "" {
		return S3Bucket{}, "", fmt.Errorf("s3 address %q: want a bucket and an object key", address)
	}
	return b, key, nil
}

// s3Region reads the region from what follows "s3" in an amazonaws.com host:
// ".amazonaws.com" (none), ".eu-west-1.amazonaws.com", "-eu-west-1.amazonaws.com"
// or ".dualstack.eu-west-1.amazonaws.com".
func s3Region(rest string) string {
	rest = strings.Trim(strings.TrimSuffix(rest, "amazonaws.com"), ".-")
	if i := strings.LastIndex(rest, "."); i >= 0 {
		rest = rest[i+1:]
	}
	if rest == "external-1" {
		return "us-east-1" // s3-external-1, the old us-east-1 endpoint
	}
	return rest
}

// s3Versions lists the versions of a module kept in S3: the objects whose
// keys match the address's key with a version in place of the placeholder.
func (r *Resolver) s3Versions(ctx context.Context, address string) ([]string, error) {
	b, key, err := ParseS3(address)
	if err != nil {
		return nil, err
	}
	prefix, suffix, ok := strings.Cut(key, source.VersionPlaceholder)
	if !ok {
		return nil, fmt.Errorf("s3://%s/%s: no version in the object key to compare", b.Name, key)
	}
	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `(v?\d+\.\d+\.\d+)` + regexp.QuoteMeta(suffix) + "$")
	list := r.S3
	if list == nil {
		list = r.listS3
	}
	keys, err := list(ctx, b, prefix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		if m := re.FindStringSubmatch(k); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("s3://%s/%s: no objects match", b.Name, key)
	}
	return out, nil
}

// listS3 lists keys with the AWS SDK, using the standard credential chain:
// environment variables, shared config and SSO profiles, web identity (such
// as GitHub Actions OIDC) and instance roles.
func (r *Resolver) listS3(ctx context.Context, b S3Bucket, prefix string) ([]string, error) {
	cfg, err := r.awsConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := func(region string) *s3.Client {
		return s3.NewFromConfig(cfg, func(o *s3.Options) {
			if b.Endpoint != "" {
				o.BaseEndpoint = aws.String(b.Endpoint)
				o.UsePathStyle = true
			}
			if region != "" {
				o.Region = region
			} else if o.Region == "" {
				o.Region = "us-east-1"
			}
		})
	}
	region := b.Region
	if region == "" && b.Endpoint == "" {
		if region, err = manager.GetBucketRegion(ctx, client(""), b.Name); err != nil {
			return nil, fmt.Errorf("s3://%s: finding its region: %w", b.Name, err)
		}
	}
	var keys []string
	pages := s3.NewListObjectsV2Paginator(client(region), &s3.ListObjectsV2Input{Bucket: aws.String(b.Name), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing s3://%s/%s: %w", b.Name, prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		if len(keys) > maxS3Keys {
			return nil, fmt.Errorf("listing s3://%s/%s: more than %d objects", b.Name, prefix, maxS3Keys)
		}
	}
	return keys, nil
}

// awsConfig loads the AWS configuration once per Resolver, on the first S3
// lookup, so runs without S3 modules never touch it. The SDK builds its own
// HTTP client, which honours AWS_CA_BUNDLE.
func (r *Resolver) awsConfig(ctx context.Context) (aws.Config, error) {
	r.awsOnce.Do(func() {
		var opts []func(*config.LoadOptions) error
		if r.awsHTTP != nil {
			opts = append(opts, config.WithHTTPClient(r.awsHTTP))
		}
		r.aws, r.awsErr = config.LoadDefaultConfig(ctx, opts...)
	})
	return r.aws, r.awsErr
}

// awsState is the AWS configuration a Resolver loads on first use.
type awsState struct {
	awsOnce sync.Once
	aws     aws.Config
	awsErr  error
	// awsHTTP replaces the SDK's HTTP client; tests set it.
	awsHTTP *http.Client
}
