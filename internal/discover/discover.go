// Package discover lists the repos to scan: from one or more GitHub orgs, with
// each org's name, topic and custom property filters applied, or from a local
// directory of clones.
package discover

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-github/v93/github"
	"golang.org/x/time/rate"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/filter"
	"github.com/rossedman/tfmetrics/internal/ghapi"
)

// Repo is a repo selected for scanning.
type Repo struct {
	FullName      string            `json:"repo"` // org/name
	Org           string            `json:"org"`
	Name          string            `json:"name"`
	CloneURL      string            `json:"clone_url,omitempty"`
	LocalPath     string            `json:"local_path,omitempty"`
	DefaultBranch string            `json:"default_branch,omitempty"`
	HeadSHA       string            `json:"head_sha,omitempty"` // default branch HEAD at discovery, if known
	Topics        []string          `json:"topics"`
	Properties    map[string]string `json:"properties"`
}

// GitHub discovers repos through the GitHub API.
type GitHub struct {
	Client *github.Client
	// Transport keeps every call within GitHub's rate limit policy and counts
	// them; read Transport.Stats() after a run.
	Transport *ghapi.Transport
	// OnWarning is told about problems that only slow a run down; may be nil.
	OnWarning func(msg string)

	http       *http.Client
	token      string
	graphqlURL string
}

// NewGitHub returns a GitHub discoverer. baseURL is empty for github.com or a
// GitHub Enterprise API URL. Calls go out one at a time, at most rps per
// second, through a rate-limit-aware transport.
func NewGitHub(token, baseURL string, rps float64) (*GitHub, error) {
	if rps <= 0 {
		rps = 10
	}
	tr := &ghapi.Transport{Base: &limitedTransport{
		base:    http.DefaultTransport,
		limiter: rate.NewLimiter(rate.Limit(rps), int(rps)+1),
	}}
	hc := &http.Client{Transport: tr}
	opts := []github.ClientOptionsFunc{
		github.WithHTTPClient(hc),
		github.WithUserAgent("tfmetrics"),
		// The transport waits out an exhausted limit instead of failing fast.
		github.WithDisableRateLimitCheck(),
	}
	if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}
	if baseURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(baseURL, baseURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &GitHub{Client: c, Transport: tr, http: hc, token: token, graphqlURL: GraphQLURL(baseURL)}, nil
}

// GraphQLURL returns the GraphQL endpoint for a REST base URL: github.com's
// for an empty base, or <host>/api/graphql for GitHub Enterprise.
func GraphQLURL(baseURL string) string {
	if baseURL == "" {
		return "https://api.github.com/graphql"
	}
	b := strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/api/v3")
	return b + "/api/graphql"
}

// limitedTransport is a token bucket in front of an http.RoundTripper.
type limitedTransport struct {
	base    http.RoundTripper
	limiter *rate.Limiter
}

// RoundTrip waits for a token, then sends the request.
func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// Org lists the repos in one org that pass its filters. Custom property
// values come from one paged call for the whole org, joined to the repo list.
func (g *GitHub) Org(ctx context.Context, org config.Org, defaults config.RepoDefault) ([]Repo, error) {
	sel, err := filter.NewSelector(org, defaults)
	if err != nil {
		return nil, err
	}
	props, err := g.properties(ctx, org.Name)
	if err != nil {
		return nil, err
	}
	var out []Repo
	opts := &github.RepositoryListByOrgOptions{Type: "all", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		repos, resp, err := g.Client.Repositories.ListByOrg(ctx, org.Name, opts)
		if err != nil {
			return nil, fmt.Errorf("list repos in %s: %w", org.Name, err)
		}
		for _, r := range repos {
			fr := filter.Repo{
				Name:       r.GetName(),
				Topics:     r.Topics,
				Properties: props[r.GetName()],
				Archived:   r.GetArchived(),
				Fork:       r.GetFork(),
			}
			if !sel.Selected(fr) {
				continue
			}
			out = append(out, Repo{
				FullName:      r.GetFullName(),
				Org:           org.Name,
				Name:          r.GetName(),
				CloneURL:      r.GetCloneURL(),
				DefaultBranch: r.GetDefaultBranch(),
				Topics:        nonNil(r.Topics),
				Properties:    flatten(fr.Properties),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullName < out[j].FullName })
	g.fillHeads(ctx, org.Name, out)
	return out, nil
}

// fillHeads sets HeadSHA on each repo from batched GraphQL lookups, so the
// cache check needs no git round trip per repo. A failure only costs speed:
// repos without a HeadSHA fall back to git ls-remote.
func (g *GitHub) fillHeads(ctx context.Context, org string, repos []Repo) {
	if g.http == nil || len(repos) == 0 {
		return
	}
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	heads, err := g.HeadSHAs(ctx, org, names)
	if err != nil && g.OnWarning != nil {
		g.OnWarning(fmt.Sprintf("head lookup in %s fell back to git ls-remote: %v", org, err))
	}
	for i := range repos {
		repos[i].HeadSHA = heads[repos[i].Name]
	}
}

// properties returns custom property values for every repo in an org, keyed
// by repo name. Orgs without custom properties (or personal accounts) return
// an empty map rather than an error.
func (g *GitHub) properties(ctx context.Context, org string) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	opts := &github.ListCustomPropertyValuesOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		vals, resp, err := g.Client.Organizations.ListCustomPropertyValues(ctx, org, opts)
		if err != nil {
			var er *github.ErrorResponse
			if errors.As(err, &er) && er.Response != nil && (er.Response.StatusCode == http.StatusNotFound || er.Response.StatusCode == http.StatusForbidden) {
				return out, nil
			}
			return nil, fmt.Errorf("list custom property values in %s: %w", org, err)
		}
		for _, v := range vals {
			m := map[string][]string{}
			for _, p := range v.Properties {
				if p == nil {
					continue
				}
				if vs := propertyValues(p.Value); len(vs) > 0 {
					m[p.PropertyName] = vs
				}
			}
			out[v.RepositoryName] = m
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// propertyValues converts a custom property value (a string, a list of
// strings for multi-select, or null) to a list.
func propertyValues(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		var out []string
		for _, s := range t {
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// flatten joins multi-select values with commas for facts and labels.
func flatten(p map[string][]string) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = strings.Join(v, ",")
	}
	return out
}

// nonNil returns s, or an empty slice when s is nil.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// LocalDir lists every git checkout directly under dir as a repo named
// <org>/<dir name>, applying the org's name filters. Topic and property
// filters need an API and so match nothing for local repos.
func LocalDir(dir, org string, f config.Filter, exclude config.Filter) ([]Repo, error) {
	sel, err := filter.NewSelector(config.Org{Name: org, Include: f, Exclude: exclude}, config.RepoDefault{})
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Repo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			continue
		}
		if !sel.Selected(filter.Repo{Name: e.Name()}) {
			continue
		}
		out = append(out, Repo{
			FullName:   org + "/" + e.Name(),
			Org:        org,
			Name:       e.Name(),
			LocalPath:  p,
			Topics:     []string{},
			Properties: map[string]string{},
		})
	}
	return out, nil
}
