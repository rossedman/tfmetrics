// Package registry looks up the available versions of a module: from the
// Terraform registry API for public modules, from the private registry for
// internal ones, from git tags for git sources, and from object keys for
// modules kept in S3. Lookups are cached for the
// life of a Resolver, which is one run.
package registry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rossedman/tfmetrics/internal/ghapi"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Lister returns the available versions of a module address.
type Lister interface {
	Versions(ctx context.Context, kind, address string) ([]string, error)
}

// GitTagLister lists the tags of a git remote.
type GitTagLister func(ctx context.Context, url string) ([]string, error)

// Resolver looks up and caches module versions.
type Resolver struct {
	HTTP *http.Client
	// Tokens maps a registry host to a bearer token. Hosts not in it use
	// Terraform's TF_TOKEN_<host> environment variable, if set.
	Tokens map[string]string
	// GitToken authenticates git tag lookups against GitTokenHost.
	GitToken     string
	GitTokenHost string
	// GitTags lists tags of a git remote; defaults to git ls-remote.
	GitTags GitTagLister
	// Scheme is the URL scheme for registry calls; "https" unless a test sets it.
	Scheme string
	// S3 lists object keys for modules kept in S3; defaults to the AWS SDK
	// with the standard credential chain.
	S3 S3Lister
	awsState

	mu       sync.Mutex
	cache    map[string]*result
	services map[string]map[string]any // discovery document per host
}

// result is one cached lookup; done closes once it is filled in.
type result struct {
	done     chan struct{}
	versions []string
	err      error
}

// New returns a Resolver with sensible defaults.
func New() *Resolver {
	// Registries are not GitHub's API, so lookups may run in parallel; they
	// still back off on 429s, Retry-After and server errors.
	tr := &ghapi.Transport{Concurrency: LookupConcurrency}
	return &Resolver{HTTP: &http.Client{Timeout: 30 * time.Second, Transport: tr}, Scheme: "https"}
}

// LookupConcurrency is how many version lookups run at once.
const LookupConcurrency = 8

// Versions returns the versions available for a module address. Addresses of
// other kinds return nil.
func (r *Resolver) Versions(ctx context.Context, kind, address string) ([]string, error) {
	// Versions belong to the module, not to a subdirectory of it, so every
	// "//subdir" of one module shares a lookup. Callers asking at the same
	// time wait for the first one instead of repeating it.
	module, _, _ := strings.Cut(address, "//")
	key := kind + " " + module
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]*result{}
	}
	if res, ok := r.cache[key]; ok {
		r.mu.Unlock()
		select {
		case <-res.done:
			return res.versions, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	res := &result{done: make(chan struct{})}
	r.cache[key] = res
	r.mu.Unlock()

	switch kind {
	case source.KindRegistry, source.KindPrivateRegistry:
		res.versions, res.err = r.registryVersions(ctx, address)
	case source.KindGit:
		res.versions, res.err = r.gitVersions(ctx, address)
	case source.KindProvider:
		res.versions, res.err = r.providerVersions(ctx, address)
	case source.KindS3:
		res.versions, res.err = r.s3Versions(ctx, address)
	}
	close(res.done)
	return res.versions, res.err
}

// registryVersions calls the module registry protocol's versions endpoint.
func (r *Resolver) registryVersions(ctx context.Context, address string) ([]string, error) {
	addr, _, _ := strings.Cut(address, "//")
	parts := strings.Split(addr, "/")
	if len(parts) != 4 {
		return nil, fmt.Errorf("registry address %q: want host/namespace/name/provider", address)
	}
	host := parts[0]
	base, err := r.serviceBase(ctx, host, "modules.v1")
	if err != nil {
		return nil, err
	}
	u := base + strings.Join(parts[1:], "/") + "/versions"
	var body struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if err := r.getJSON(ctx, host, u, &body); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range body.Modules {
		for _, v := range m.Versions {
			out = append(out, v.Version)
		}
	}
	return out, nil
}

// providerVersions calls the provider registry protocol's versions endpoint
// for a host/namespace/type address.
func (r *Resolver) providerVersions(ctx context.Context, address string) ([]string, error) {
	parts := strings.Split(address, "/")
	if len(parts) != 3 {
		return nil, fmt.Errorf("provider address %q: want host/namespace/type", address)
	}
	base, err := r.serviceBase(ctx, parts[0], "providers.v1")
	if err != nil {
		return nil, err
	}
	var body struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	if err := r.getJSON(ctx, parts[0], base+parts[1]+"/"+parts[2]+"/versions", &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Versions))
	for _, v := range body.Versions {
		out = append(out, v.Version)
	}
	return out, nil
}

// serviceBase returns the base URL of a registry service, such as
// "modules.v1", from host's service discovery document, which is fetched once
// per run.
func (r *Resolver) serviceBase(ctx context.Context, host, service string) (string, error) {
	r.mu.Lock()
	if r.services == nil {
		r.services = map[string]map[string]any{}
	}
	disco, ok := r.services[host]
	r.mu.Unlock()
	root := r.scheme() + "://" + host
	if !ok {
		if err := r.getJSON(ctx, host, root+"/.well-known/terraform.json", &disco); err != nil {
			return "", fmt.Errorf("service discovery for %s: %w", host, err)
		}
		r.mu.Lock()
		r.services[host] = disco
		r.mu.Unlock()
	}
	p, _ := disco[service].(string)
	if p == "" {
		return "", fmt.Errorf("service discovery for %s: no %s", host, service)
	}
	base := p
	if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		base = root + "/" + strings.TrimPrefix(p, "/")
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base, nil
}

// scheme returns the URL scheme for registry calls.
func (r *Resolver) scheme() string {
	if r.Scheme == "" {
		return "https"
	}
	return r.Scheme
}

// getJSON GETs a URL and decodes the JSON response.
func (r *Resolver) getJSON(ctx context.Context, host, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if t := r.token(host); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// The status leads, so a message cut to one line still says what went
		// wrong.
		if detail := errorDetail(resp.Header.Get("Content-Type"), b); detail != "" {
			return fmt.Errorf("%s: %s (GET %s)", resp.Status, detail, u)
		}
		return fmt.Errorf("%s (GET %s)", resp.Status, u)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// token returns the bearer token for a registry host: the configured one, or
// else the TF_TOKEN_<host> variable the Terraform CLI itself reads.
func (r *Resolver) token(host string) string {
	if t := r.Tokens[host]; t != "" {
		return t
	}
	return os.Getenv(TerraformTokenEnv(host))
}

// TerraformTokenEnv returns the name of the environment variable the Terraform
// CLI reads a host's token from: TF_TOKEN_ plus the host with "." written as
// "_" and "-" as "__", such as TF_TOKEN_app_terraform_io.
func TerraformTokenEnv(host string) string {
	h := strings.ReplaceAll(strings.ToLower(host), "-", "__")
	return "TF_TOKEN_" + strings.ReplaceAll(h, ".", "_")
}

// errorDetail returns an error response body worth showing: JSON or plain
// text, on one line and at most 200 bytes. HTML pages, such as a web server's
// 404 page on a host that is not a registry, say nothing useful.
func errorDetail(contentType string, body []byte) string {
	if strings.Contains(contentType, "html") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
		return ""
	}
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "..."
	}
	return s
}

// gitVersions lists the semver-looking tags of a git address.
func (r *Resolver) gitVersions(ctx context.Context, address string) ([]string, error) {
	repo, _, _ := strings.Cut(address, "//")
	url := "https://" + repo + ".git"
	list := r.GitTags
	if list == nil {
		list = r.lsRemoteTags
	}
	return list(ctx, url)
}

// lsRemoteTags lists tags with git ls-remote, authenticating to GitTokenHost.
func (r *Resolver) lsRemoteTags(ctx context.Context, url string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", url)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	if r.GitToken != "" && r.GitTokenHost != "" {
		cmd.Env = AuthEnv(cmd.Env, r.GitTokenHost, r.GitToken)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %w", url, err)
	}
	return ParseTags(out), nil
}

// ParseTags extracts tag names from git ls-remote --tags output.
func ParseTags(out []byte) []string {
	var tags []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		_, ref, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		for strings.HasSuffix(ref, "^{}") {
			ref = strings.TrimSuffix(ref, "^{}")
		}
		// Skip empty names, and names that still look like refs; git forbids
		// "^" in tag names so these only come from malformed output.
		if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok && t != "" && !strings.HasPrefix(t, "refs/") {
			tags = append(tags, t)
		}
	}
	return tags
}

// AuthEnv returns env with git config entries added that make git send a token
// to host without putting it in a URL or on the command line. Entries already
// in env through GIT_CONFIG_COUNT are kept: the new one is numbered after them.
func AuthEnv(env []string, host, token string) []string {
	n := 0
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			n, _ = strconv.Atoi(v) // the last one wins, as it does for exec
		}
	}
	if n < 0 {
		n = 0
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.https://%s/.extraheader", n, host),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=AUTHORIZATION: basic %s", n, cred),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1),
	)
}
