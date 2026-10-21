package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
)

func result(repo, commit string) facts.ScanResult {
	return facts.ScanResult{
		SchemaVersion: facts.SchemaVersion,
		Repo:          repo,
		Org:           "acme",
		Topics:        []string{},
		Properties:    map[string]string{},
		Commit:        commit,
		Owners:        []string{"platform"},
		Modules:       []facts.ModuleFacts{},
	}
}

func TestKey(t *testing.T) {
	t.Parallel()
	props := map[string]string{"owner": "x", "tier": "gold"}
	base := Key("abc123", []string{"b", "a"}, props, "s1")
	if !strings.HasPrefix(base, "abc123-") || len(base) != len("abc123-")+12 {
		t.Errorf("unexpected key shape %q", base)
	}
	tests := []struct {
		name string
		key  string
		same bool
	}{
		{"topic order independent", Key("abc123", []string{"a", "b"}, props, "s1"), true},
		{"repeatable", Key("abc123", []string{"b", "a"}, map[string]string{"tier": "gold", "owner": "x"}, "s1"), true},
		{"commit changes key", Key("def456", []string{"b", "a"}, props, "s1"), false},
		{"topic change", Key("abc123", []string{"a", "c"}, props, "s1"), false},
		{"property change", Key("abc123", []string{"a", "b"}, map[string]string{"owner": "y"}, "s1"), false},
		{"settings change", Key("abc123", []string{"a", "b"}, props, "s2"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if (tt.key == base) != tt.same {
				t.Errorf("key %q vs base %q: same=%v, want %v", tt.key, base, tt.key == base, tt.same)
			}
		})
	}
}

func TestKeyDoesNotMutateTopics(t *testing.T) {
	t.Parallel()
	topics := []string{"b", "a"}
	Key("c", topics, nil, "")
	if topics[0] != "b" || topics[1] != "a" {
		t.Errorf("Key reordered caller's slice: %v", topics)
	}
}

func TestPutGet(t *testing.T) {
	t.Parallel()
	c := Cache{Dir: t.TempDir()}
	if _, ok := c.Get("acme/net", "k1"); ok {
		t.Fatal("hit on empty cache")
	}
	if err := c.Put("k1", result("acme/net", "c1")); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("acme/net", "k1")
	if !ok || got.Commit != "c1" || got.Repo != "acme/net" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if _, ok := c.Get("acme/other", "k1"); ok {
		t.Error("hit for a different repo")
	}
}

func TestPutReplacesOldEntries(t *testing.T) {
	t.Parallel()
	c := Cache{Dir: t.TempDir()}
	for _, k := range []string{"k1", "k2"} {
		if err := c.Put(k, result("acme/net", "commit-"+k)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := c.Get("acme/net", "k1"); ok {
		t.Error("old entry k1 should have been replaced")
	}
	if got, ok := c.Get("acme/net", "k2"); !ok || got.Commit != "commit-k2" {
		t.Errorf("k2 = %+v, %v", got, ok)
	}
	// Other repos are untouched.
	if err := c.Put("k1", result("acme/dns", "d1")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("k3", result("acme/net", "n3")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("acme/dns", "k1"); !ok {
		t.Error("putting acme/net evicted acme/dns")
	}
	files, _ := filepath.Glob(filepath.Join(c.Dir, "acme__net", "*.json"))
	if len(files) != 1 {
		t.Errorf("want 1 file for acme/net, got %v", files)
	}
}

func TestDisabledCache(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    Cache
		key  string
	}{
		{"no dir", Cache{}, "k"},
		{"no key", Cache{Dir: t.TempDir()}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.c.Put(tt.key, result("acme/net", "c")); err != nil {
				t.Errorf("Put: %v", err)
			}
			if _, ok := tt.c.Get("acme/net", tt.key); ok {
				t.Error("Get hit on disabled cache")
			}
		})
	}
	// Nothing was written for the no-key case.
	d := t.TempDir()
	_ = Cache{Dir: d}.Put("", result("acme/net", "c"))
	if entries, _ := os.ReadDir(d); len(entries) != 0 {
		t.Errorf("wrote %d entries with empty key", len(entries))
	}
}

func TestGetCorruptOrWrongSchema(t *testing.T) {
	t.Parallel()
	c := Cache{Dir: t.TempDir()}
	dir := filepath.Join(c.Dir, "acme__net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{"schema_version": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bad", "old"} {
		if _, ok := c.Get("acme/net", k); ok {
			t.Errorf("Get(%s) hit on unreadable entry", k)
		}
	}
}

func TestPutErrors(t *testing.T) {
	t.Parallel()
	// Dir is a file, so MkdirAll fails.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Cache{Dir: f}).Put("k", result("acme/net", "c")); err == nil {
		t.Error("expected error when cache dir is a file")
	}
}

func BenchmarkKey(b *testing.B) {
	props := map[string]string{"owner": "x", "tier": "gold"}
	topics := []string{"terraform", "team-platform", "aws"}
	b.ReportAllocs()
	for b.Loop() {
		_ = Key("0123456789abcdef", topics, props, "ignore=examples")
	}
}
