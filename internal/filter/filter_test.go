package filter

import (
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/config"
)

func mustCompile(t testing.TB, f config.Filter) *Matcher {
	t.Helper()
	m, err := Compile(f)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMatches(t *testing.T) {
	t.Parallel()
	repo := Repo{
		Name:   "tf-network",
		Topics: []string{"Terraform", "team-platform"},
		Properties: map[string][]string{
			"tier": {"gold"},
			"env":  {"prod", "staging"},
		},
	}
	tests := []struct {
		name string
		f    config.Filter
		want bool
	}{
		{"empty matches all", config.Filter{}, true},
		{"glob name", config.Filter{Names: []string{"tf-*"}}, true},
		{"glob name miss", config.Filter{Names: []string{"infra-*"}}, false},
		{"names are OR", config.Filter{Names: []string{"infra-*", "tf-network"}}, true},
		{"regex name", config.Filter{Names: []string{`re:^tf-(network|dns)$`}}, true},
		{"regex unanchored", config.Filter{Names: []string{`re:work`}}, true},
		{"regex miss", config.Filter{Names: []string{`re:^dns`}}, false},
		{"mixed glob and regex OR", config.Filter{Names: []string{"nope", `re:net`}}, true},
		{"topic case-insensitive", config.Filter{Topics: []string{"terraform"}}, true},
		{"topic miss", config.Filter{Topics: []string{"kubernetes"}}, false},
		{"topics OR", config.Filter{Topics: []string{"kubernetes", "TEAM-PLATFORM"}}, true},
		{"property match", config.Filter{Properties: map[string][]string{"tier": {"gold"}}}, true},
		{"property value OR", config.Filter{Properties: map[string][]string{"tier": {"silver", "gold"}}}, true},
		{"property multi-select repo", config.Filter{Properties: map[string][]string{"env": {"staging"}}}, true},
		{"property value miss", config.Filter{Properties: map[string][]string{"tier": {"silver"}}}, false},
		{"property case-sensitive", config.Filter{Properties: map[string][]string{"tier": {"Gold"}}}, false},
		{"missing property key", config.Filter{Properties: map[string][]string{"owner": {"x"}}}, false},
		{"properties are AND", config.Filter{Properties: map[string][]string{"tier": {"gold"}, "owner": {"x"}}}, false},
		{"all kinds AND ok", config.Filter{Names: []string{"tf-*"}, Topics: []string{"terraform"}, Properties: map[string][]string{"tier": {"gold"}}}, true},
		{"all kinds AND one fails", config.Filter{Names: []string{"tf-*"}, Topics: []string{"nope"}, Properties: map[string][]string{"tier": {"gold"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mustCompile(t, tt.f).Matches(repo); got != tt.want {
				t.Errorf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesNoProperties(t *testing.T) {
	t.Parallel()
	m := mustCompile(t, config.Filter{Properties: map[string][]string{"tier": {"gold"}}})
	if m.Matches(Repo{Name: "x"}) {
		t.Error("repo without properties must not match a property filter")
	}
}

func TestCompileErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    config.Filter
		want string
	}{
		{"bad regex", config.Filter{Names: []string{"re:("}}, "name pattern"},
		{"bad glob", config.Filter{Names: []string{"[abc"}}, "name pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(tt.f)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestEmpty(t *testing.T) {
	t.Parallel()
	if !mustCompile(t, config.Filter{}).Empty() {
		t.Error("empty filter should be Empty")
	}
	if mustCompile(t, config.Filter{Topics: []string{"a"}}).Empty() {
		t.Error("filter with topics should not be Empty")
	}
}

func TestSelector(t *testing.T) {
	t.Parallel()
	base := Repo{Name: "tf-network", Topics: []string{"terraform"}}
	tests := []struct {
		name     string
		org      config.Org
		defaults config.RepoDefault
		repo     Repo
		want     bool
	}{
		{"no filters selects all", config.Org{Name: "acme"}, config.RepoDefault{}, base, true},
		{"include match", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"terraform"}}}, config.RepoDefault{}, base, true},
		{"include miss", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"k8s"}}}, config.RepoDefault{}, base, false},
		{"exclude wins", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"terraform"}}, Exclude: config.Filter{Names: []string{"tf-net*"}}}, config.RepoDefault{}, base, false},
		{"exclude miss keeps repo", config.Org{Name: "acme", Exclude: config.Filter{Names: []string{"other"}}}, config.RepoDefault{}, base, true},
		{"empty exclude excludes nothing", config.Org{Name: "acme", Exclude: config.Filter{}}, config.RepoDefault{}, base, true},
		{"archived skipped by default", config.Org{Name: "acme"}, config.RepoDefault{}, Repo{Name: "a", Archived: true}, false},
		{"archived allowed", config.Org{Name: "acme"}, config.RepoDefault{Archived: true}, Repo{Name: "a", Archived: true}, true},
		{"fork skipped by default", config.Org{Name: "acme"}, config.RepoDefault{}, Repo{Name: "a", Fork: true}, false},
		{"fork allowed", config.Org{Name: "acme"}, config.RepoDefault{Forks: true}, Repo{Name: "a", Fork: true}, true},
		{"archived allowed but excluded", config.Org{Name: "acme", Exclude: config.Filter{Names: []string{"a"}}}, config.RepoDefault{Archived: true}, Repo{Name: "a", Archived: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, err := NewSelector(tt.org, tt.defaults)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Selected(tt.repo); got != tt.want {
				t.Errorf("Selected = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewSelectorErrors(t *testing.T) {
	t.Parallel()
	_, err := NewSelector(config.Org{Name: "acme", Include: config.Filter{Names: []string{"re:("}}}, config.RepoDefault{})
	if err == nil || !strings.Contains(err.Error(), "org acme include") {
		t.Errorf("include err = %v", err)
	}
	_, err = NewSelector(config.Org{Name: "acme", Exclude: config.Filter{Names: []string{"re:("}}}, config.RepoDefault{})
	if err == nil || !strings.Contains(err.Error(), "org acme exclude") {
		t.Errorf("exclude err = %v", err)
	}
}

func TestParseProperty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		key     string
		vals    []string
		wantErr bool
	}{
		{"tier=gold", "tier", []string{"gold"}, false},
		{" tier = gold , silver ", "tier", []string{"gold", "silver"}, false},
		{"tier=a,,b,", "tier", []string{"a", "b"}, false},
		{"k=a=b", "k", []string{"a=b"}, false},
		{"tier", "", nil, true},
		{"=gold", "", nil, true},
		{"tier=", "", nil, true},
		{"tier=  ", "", nil, true},
		{"tier=,", "", nil, true},
		{"tier= , ,", "", nil, true},
		{"", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			k, v, err := ParseProperty(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if k != tt.key || strings.Join(v, "|") != strings.Join(tt.vals, "|") {
				t.Errorf("got %q %q, want %q %q", k, v, tt.key, tt.vals)
			}
		})
	}
}

func FuzzCompileMatches(f *testing.F) {
	for _, s := range []string{"tf-*", "re:^tf-.*$", "re:(", "[abc", "*", "", "a?b", `\`, "re:", "tf-network", "[a-", "re:(?i)NET"} {
		f.Add(s, "tf-network", "Terraform")
	}
	f.Fuzz(func(t *testing.T, pattern, name, topic string) {
		m, err := Compile(config.Filter{Names: []string{pattern}, Topics: []string{topic}})
		if err != nil {
			if m != nil {
				t.Fatal("non-nil matcher with error")
			}
			return
		}
		r := Repo{Name: name, Topics: []string{topic}}
		got := m.Matches(r)
		// Matching is deterministic.
		if m.Matches(r) != got {
			t.Fatal("nondeterministic Matches")
		}
		// A topic filter is case-insensitive.
		r2 := Repo{Name: name, Topics: []string{strings.ToUpper(topic)}}
		if strings.ToLower(strings.ToUpper(topic)) == strings.ToLower(topic) {
			tm := mustCompile(t, config.Filter{Topics: []string{topic}})
			if !tm.Matches(r2) {
				t.Fatalf("topic %q not matched case-insensitively", topic)
			}
		}
		// An empty-name-filter matcher accepts everything.
		if !mustCompile(t, config.Filter{}).Matches(r) {
			t.Fatal("empty filter must match")
		}
		// The same pattern in a selector must not panic and exclude must win.
		s, err := NewSelector(config.Org{Name: "o", Include: config.Filter{}, Exclude: config.Filter{Names: []string{pattern}}}, config.RepoDefault{Archived: true, Forks: true})
		if err != nil {
			t.Fatalf("selector rejected pattern the matcher accepted: %v", err)
		}
		if len(pattern) > 0 && m2(t, pattern).Matches(Repo{Name: name}) && s.Selected(Repo{Name: name}) {
			t.Fatalf("exclude did not win for %q", name)
		}
	})
}

func m2(t testing.TB, pattern string) *Matcher {
	return mustCompile(t, config.Filter{Names: []string{pattern}})
}

func FuzzParseProperty(f *testing.F) {
	for _, s := range []string{"a=b", "a=b,c", "", "=", "a=", "=b", " a = b "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k, vals, err := ParseProperty(s)
		if err != nil {
			return
		}
		if k == "" || len(vals) == 0 {
			t.Fatalf("bad result k=%q vals=%q for %q", k, vals, s)
		}
		for _, v := range vals {
			if v == "" || v != strings.TrimSpace(v) {
				t.Fatalf("untrimmed/empty value %q", v)
			}
		}
	})
}

func BenchmarkSelected(b *testing.B) {
	s, err := NewSelector(config.Org{
		Name:    "acme",
		Include: config.Filter{Names: []string{"tf-*", "re:^infra-.*$"}, Topics: []string{"terraform"}, Properties: map[string][]string{"tier": {"gold", "silver"}}},
		Exclude: config.Filter{Names: []string{"tf-sandbox*"}},
	}, config.RepoDefault{})
	if err != nil {
		b.Fatal(err)
	}
	r := Repo{Name: "tf-network", Topics: []string{"terraform", "team-platform"}, Properties: map[string][]string{"tier": {"gold"}}}
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Selected(r)
	}
}
