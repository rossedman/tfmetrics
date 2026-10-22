package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/source"
)

func TestParseS3(t *testing.T) {
	t.Parallel()
	tests := []struct {
		address string
		bucket  S3Bucket
		key     string
	}{
		{"acme-tf.s3.amazonaws.com/vpc/*/vpc.zip", S3Bucket{Name: "acme-tf"}, "vpc/*/vpc.zip"},
		{"acme-tf.s3.eu-west-1.amazonaws.com/vpc-*.zip", S3Bucket{Name: "acme-tf", Region: "eu-west-1"}, "vpc-*.zip"},
		{"acme-tf.s3-eu-west-1.amazonaws.com/vpc-*.zip", S3Bucket{Name: "acme-tf", Region: "eu-west-1"}, "vpc-*.zip"},
		{"my.dotted.bucket.s3.us-west-2.amazonaws.com/k", S3Bucket{Name: "my.dotted.bucket", Region: "us-west-2"}, "k"},
		{"acme-tf.s3.dualstack.ap-south-1.amazonaws.com/k", S3Bucket{Name: "acme-tf", Region: "ap-south-1"}, "k"},
		{"s3.amazonaws.com/acme-tf/mods/vpc-*.zip", S3Bucket{Name: "acme-tf"}, "mods/vpc-*.zip"},
		{"s3-eu-west-1.amazonaws.com/acme-tf/vpc/*/vpc.zip", S3Bucket{Name: "acme-tf", Region: "eu-west-1"}, "vpc/*/vpc.zip"},
		{"s3.eu-central-1.amazonaws.com/acme-tf/k", S3Bucket{Name: "acme-tf", Region: "eu-central-1"}, "k"},
		{"s3-external-1.amazonaws.com/acme-tf/k", S3Bucket{Name: "acme-tf", Region: "us-east-1"}, "k"},
		{"minio.acme.io:9000/tf/vpc-*.zip//sub", S3Bucket{Name: "tf", Endpoint: "https://minio.acme.io:9000"}, "vpc-*.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			t.Parallel()
			b, key, err := ParseS3(tt.address)
			if err != nil || b != tt.bucket || key != tt.key {
				t.Errorf("ParseS3 = %+v, %q, %v; want %+v, %q", b, key, err, tt.bucket, tt.key)
			}
		})
	}
	for _, bad := range []string{"s3.amazonaws.com/bucket-only", "s3.amazonaws.com", "acme.s3.amazonaws.com/", "minio.local/", "ec2.amazonaws.com/b/k"} {
		if _, _, err := ParseS3(bad); err == nil {
			t.Errorf("ParseS3(%q) should fail", bad)
		}
	}
}

func TestS3Versions(t *testing.T) {
	t.Parallel()
	var asked []string
	r := newResolver()
	r.S3 = func(_ context.Context, b S3Bucket, prefix string) ([]string, error) {
		asked = append(asked, b.Name+" "+b.Region+" "+prefix)
		switch b.Name {
		case "broken":
			return nil, errors.New("access denied")
		case "empty":
			return nil, nil
		}
		return []string{
			"vpc/v1.0.0/vpc.zip", "vpc/v1.2.0/vpc.zip", "vpc/v2.0.0/vpc.zip",
			"vpc/v2.1.0-rc.1/vpc.zip", // not a release key shape
			"vpc/latest/vpc.zip", "vpc/v1.2.0/README.md", "vpc/v1.2.0/vpc.zip.sha256",
		}, nil
	}
	ctx := context.Background()
	got, err := r.Versions(ctx, source.KindS3, "acme-tf.s3.eu-west-1.amazonaws.com/vpc/*/vpc.zip//modules/a")
	if err != nil || !slices.Equal(got, []string{"v1.0.0", "v1.2.0", "v2.0.0"}) {
		t.Fatalf("versions = %v, %v", got, err)
	}
	if !slices.Equal(asked, []string{"acme-tf eu-west-1 vpc/"}) {
		t.Errorf("listed %v", asked)
	}
	for addr, want := range map[string]string{
		"broken.s3.amazonaws.com/vpc/*/vpc.zip": "access denied",
		"empty.s3.amazonaws.com/vpc/*/vpc.zip":  "s3://empty/vpc/*/vpc.zip: no objects match",
		"acme-tf.s3.amazonaws.com/vpc.zip":      "no version in the object key",
		"s3.amazonaws.com/acme-tf":              "want a bucket and an object key",
	} {
		if _, err := r.Versions(ctx, source.KindS3, addr); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", addr, err, want)
		}
	}
}

// fakeS3 serves ListObjectsV2 for one bucket, two keys per page, and records
// whether requests were signed.
func fakeS3(t *testing.T, keys []string) (*httptest.Server, *[]string) {
	t.Helper()
	var auth []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.Path != "/tf" || r.URL.Query().Get("list-type") != "2" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>NoSuchBucket</Code><Message>no such bucket</Message></Error>`)
			return
		}
		prefix := r.URL.Query().Get("prefix")
		var matched []string
		for _, k := range keys {
			if strings.HasPrefix(k, prefix) {
				matched = append(matched, k)
			}
		}
		start := 0
		if tok := r.URL.Query().Get("continuation-token"); tok != "" {
			fmt.Sscan(tok, &start)
		}
		end := min(start+2, len(matched))
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>tf</Name>`)
		for _, k := range matched[start:end] {
			fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", k)
		}
		if end < len(matched) {
			fmt.Fprintf(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
		} else {
			fmt.Fprint(w, "<IsTruncated>false</IsTruncated>")
		}
		fmt.Fprint(w, "</ListBucketResult>")
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

// isolateAWS points the AWS SDK at test credentials and away from any real
// configuration on the machine.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CA_BUNDLE", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
}

func TestS3VersionsWithSDK(t *testing.T) {
	isolateAWS(t)
	srv, auth := fakeS3(t, []string{"vpc-1.0.0.zip", "vpc-1.1.0.zip", "vpc-2.0.0.zip", "vpc-notes.txt", "other-9.9.9.zip"})
	r := New()
	r.awsHTTP = srv.Client()
	host := strings.TrimPrefix(srv.URL, "https://")
	got, err := r.Versions(context.Background(), source.KindS3, host+"/tf/vpc-*.zip")
	if err != nil || !slices.Equal(got, []string{"1.0.0", "1.1.0", "2.0.0"}) {
		t.Fatalf("versions = %v, %v", got, err)
	}
	if len(*auth) != 2 {
		t.Errorf("want two pages, got %d requests", len(*auth))
	}
	for _, a := range *auth {
		if !strings.Contains(a, "Credential=AKIDTEST/") || !strings.Contains(a, "/eu-west-1/s3/") {
			t.Errorf("request not signed for eu-west-1: %q", a)
		}
	}
	_, err = r.Versions(context.Background(), source.KindS3, host+"/missing/vpc-*.zip")
	if err == nil || !strings.Contains(err.Error(), "listing s3://missing/vpc-: ") || !strings.Contains(err.Error(), "NoSuchBucket") {
		t.Errorf("missing bucket: %v", err)
	}
}

func TestS3TooManyKeys(t *testing.T) {
	isolateAWS(t)
	keys := make([]string, 0, maxS3Keys+2)
	for i := range maxS3Keys + 2 {
		keys = append(keys, fmt.Sprintf("m/%d", i))
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<ListBucketResult><Name>tf</Name>`)
		for _, k := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", k)
		}
		fmt.Fprint(w, `<IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	defer srv.Close()
	r := New()
	r.awsHTTP = srv.Client()
	_, err := r.listS3(context.Background(), S3Bucket{Name: "tf", Endpoint: srv.URL}, "m/")
	if err == nil || !strings.Contains(err.Error(), "more than 100000 objects") {
		t.Errorf("err = %v", err)
	}
}

func TestS3RegionLookupFails(t *testing.T) {
	isolateAWS(t)
	// A bucket with no region in its address is located first; with no
	// network to AWS here, that fails cleanly instead of listing.
	r := New()
	r.awsHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	_, err := r.listS3(context.Background(), S3Bucket{Name: "acme-tf"}, "vpc/")
	if err == nil || !strings.Contains(err.Error(), "s3://acme-tf: finding its region") {
		t.Errorf("err = %v", err)
	}
}

// roundTripFunc is an http.RoundTripper made from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls f.
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func FuzzParseS3(f *testing.F) {
	for _, s := range []string{"acme.s3.amazonaws.com/vpc/*/vpc.zip", "s3-eu-west-1.amazonaws.com/b/k", "minio:9000/b/k//x", "", "/", ".s3.amazonaws.com/k"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, address string) {
		b, key, err := ParseS3(address)
		if err != nil {
			return
		}
		if b.Name == "" || key == "" {
			t.Fatalf("ParseS3(%q) = %+v, %q with no error", address, b, key)
		}
		if strings.Contains(key, "//") && !strings.Contains(address, "///") {
			t.Fatalf("subdir left in key %q", key)
		}
	})
}

func BenchmarkS3Versions(b *testing.B) {
	keys := make([]string, 0, 1000)
	for i := range 1000 {
		keys = append(keys, fmt.Sprintf("vpc/v1.%d.0/vpc.zip", i))
	}
	list := func(context.Context, S3Bucket, string) ([]string, error) { return keys, nil }
	b.ReportAllocs()
	for b.Loop() {
		r := &Resolver{S3: list}
		if _, err := r.s3Versions(context.Background(), "acme.s3.amazonaws.com/vpc/*/vpc.zip"); err != nil {
			b.Fatal(err)
		}
	}
}
