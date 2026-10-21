// Package filter decides which repos in an org are scanned. Inside include,
// every filter kind must match (AND); values within one kind are any-of (OR).
// Exclude always wins over include.
package filter

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/rossedman/tfmetrics/internal/config"
)

// Repo is what filters look at.
type Repo struct {
	Name       string              // repo name without the org
	Topics     []string            // repo topics
	Properties map[string][]string // custom properties; single values are one-element lists
	Archived   bool
	Fork       bool
}

// Matcher is a compiled filter.
type Matcher struct {
	names      []nameMatcher
	topics     map[string]bool
	properties map[string]map[string]bool
	empty      bool
}

// nameMatcher matches one name pattern.
type nameMatcher func(string) bool

// Compile compiles a config filter. Names are globs, or regexes with a re: prefix.
func Compile(f config.Filter) (*Matcher, error) {
	m := &Matcher{topics: map[string]bool{}, properties: map[string]map[string]bool{}, empty: f.Empty()}
	for _, n := range f.Names {
		if expr, ok := strings.CutPrefix(n, "re:"); ok {
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, fmt.Errorf("name pattern %q: %w", n, err)
			}
			m.names = append(m.names, re.MatchString)
			continue
		}
		if _, err := path.Match(n, ""); err != nil {
			return nil, fmt.Errorf("name pattern %q: %w", n, err)
		}
		glob := n
		m.names = append(m.names, func(s string) bool {
			ok, _ := path.Match(glob, s)
			return ok
		})
	}
	for _, t := range f.Topics {
		m.topics[strings.ToLower(t)] = true
	}
	for k, vals := range f.Properties {
		set := map[string]bool{}
		for _, v := range vals {
			set[v] = true
		}
		m.properties[k] = set
	}
	return m, nil
}

// Matches reports whether every filter kind set on m matches r. An empty
// filter matches everything when used as include, nothing when used as exclude;
// callers handle that through Empty.
func (m *Matcher) Matches(r Repo) bool {
	if len(m.names) > 0 {
		ok := false
		for _, fn := range m.names {
			if fn(r.Name) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(m.topics) > 0 {
		ok := false
		for _, t := range r.Topics {
			if m.topics[strings.ToLower(t)] {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for key, allowed := range m.properties {
		ok := false
		for _, v := range r.Properties[key] {
			if allowed[v] {
				ok = true
				break
			}
		}
		if !ok {
			return false // a repo with no value for a required key does not match
		}
	}
	return true
}

// Empty reports whether the filter sets nothing.
func (m *Matcher) Empty() bool { return m.empty }

// Selector applies one org's include and exclude filters and defaults.
type Selector struct {
	include, exclude *Matcher
	defaults         config.RepoDefault
}

// NewSelector compiles the filters for one org.
func NewSelector(org config.Org, defaults config.RepoDefault) (*Selector, error) {
	inc, err := Compile(org.Include)
	if err != nil {
		return nil, fmt.Errorf("org %s include: %w", org.Name, err)
	}
	exc, err := Compile(org.Exclude)
	if err != nil {
		return nil, fmt.Errorf("org %s exclude: %w", org.Name, err)
	}
	return &Selector{include: inc, exclude: exc, defaults: defaults}, nil
}

// Selected reports whether a repo is scanned.
func (s *Selector) Selected(r Repo) bool {
	if r.Archived && !s.defaults.Archived {
		return false
	}
	if r.Fork && !s.defaults.Forks {
		return false
	}
	if !s.exclude.Empty() && s.exclude.Matches(r) {
		return false
	}
	return s.include.Matches(r)
}

// ParseProperty parses a key=v1,v2 flag value.
func ParseProperty(s string) (string, []string, error) {
	k, v, ok := strings.Cut(s, "=")
	k = strings.TrimSpace(k)
	var vals []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			vals = append(vals, p)
		}
	}
	if !ok || k == "" || len(vals) == 0 {
		return "", nil, fmt.Errorf("property filter %q: want key=value[,value]", s)
	}
	return k, vals, nil
}
