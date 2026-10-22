// Package registry looks up the available versions of a module: from the
// Terraform registry API for public modules, from the private registry for
// internal ones, and from git tags for git sources. Lookups are cached for the
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
	// Tokens maps a registry host to a bearer token.
	Tokens map[string]string
	// GitToken authenticates git tag lookups against GitTokenHost.
	GitToken     string
	GitTokenHost string
	// GitTags lists tags of a git remote; defaults to git ls-remote.
	GitTags GitTagLister
	// Scheme is the URL scheme for registry calls; "https" unless a test sets it.
	Scheme string

	mu       sync.Mutex
	cache    map[string]result
	services map[string]string
}

// result is one cached lookup.
type result struct {
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
	key := kind + " " + address
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]result{}
	}
	if res, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return res.versions, res.err
	}
	r.mu.Unlock()

	var res result
	switch kind {
	case source.KindRegistry, source.KindPrivateRegistry:
		res.versions, res.err = r.registryVersions(ctx, address)
	case source.KindGit:
		res.versions, res.err = r.gitVersions(ctx, address)
	}

	r.mu.Lock()
	r.cache[key] = res
	r.mu.Unlock()
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
	base, err := r.modulesBase(ctx, host)
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

// modulesBase returns the modules.v1 base URL for a host via service discovery.
func (r *Resolver) modulesBase(ctx context.Context, host string) (string, error) {
	r.mu.Lock()
	if r.services == nil {
		r.services = map[string]string{}
	}
	if b, ok := r.services[host]; ok {
		r.mu.Unlock()
		return b, nil
	}
	r.mu.Unlock()

	root := r.scheme() + "://" + host
	var disco map[string]any
	if err := r.getJSON(ctx, host, root+"/.well-known/terraform.json", &disco); err != nil {
		return "", fmt.Errorf("service discovery for %s: %w", host, err)
	}
	p, _ := disco["modules.v1"].(string)
	if p == "" {
		return "", fmt.Errorf("service discovery for %s: no modules.v1", host)
	}
	base := p
	if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		base = root + "/" + strings.TrimPrefix(p, "/")
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	r.mu.Lock()
	r.services[host] = base
	r.mu.Unlock()
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
	if t := r.Tokens[host]; t != "" {
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
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
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
