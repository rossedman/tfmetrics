package versions

import (
	"testing"

	goversion "github.com/hashicorp/go-version"
)

func strs(vs []*goversion.Version) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Original()
	}
	return out
}

func mustVer(t testing.TB, s string) *goversion.Version {
	t.Helper()
	v, err := goversion.NewVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParse(t *testing.T) {
	t.Parallel()
	got := strs(Parse([]string{"2.0.0", " v1.3.0 ", "not-a-version", "", "1.2.9", "1.2.3", "main"}))
	want := []string{"1.2.3", "1.2.9", "v1.3.0", "2.0.0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if out := Parse(nil); out == nil || len(out) != 0 {
		t.Errorf("Parse(nil) = %#v, want empty non-nil", out)
	}
}

func TestLatest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"highest", []string{"1.0.0", "1.4.1", "1.2.9"}, "1.4.1"},
		{"skips prerelease", []string{"1.0.0", "2.0.0-rc.1", "1.4.1"}, "1.4.1"},
		{"only prereleases", []string{"1.0.0-beta", "2.0.0-rc.1"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Latest(Parse(tt.in))
			switch {
			case tt.want == "" && got != nil:
				t.Errorf("got %v, want nil", got)
			case tt.want != "" && (got == nil || got.Original() != tt.want):
				t.Errorf("got %v, want %s", got, tt.want)
			}
		})
	}
}

func TestSelected(t *testing.T) {
	t.Parallel()
	avail := Parse([]string{"1.0.0", "1.2.3", "1.2.9", "1.3.0", "1.4.1", "1.5.0-rc.1", "2.0.0"})
	tests := []struct {
		name       string
		constraint string
		want       string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"exact", "1.2.3", "1.2.3"},
		{"exact equals", "= 1.2.3", "1.2.3"},
		{"exact not in list", "0.9.0", "0.9.0"},
		{"pessimistic minor", "~> 1.2", "1.4.1"},
		{"pessimistic patch", "~> 1.2.3", "1.2.9"},
		{"range", ">= 1.0, < 1.3", "1.2.9"},
		{"upper bound", "< 2.0.0", "1.4.1"},
		{"gte", ">= 1.0", "2.0.0"},
		{"prerelease ignored", "~> 1.5", ""},
		{"matches nothing", "> 3.0", ""},
		{"invalid", "~~ nonsense", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Selected(tt.constraint, avail)
			switch {
			case tt.want == "" && got != nil:
				t.Errorf("Selected(%q) = %v, want nil", tt.constraint, got)
			case tt.want != "" && (got == nil || got.String() != tt.want):
				t.Errorf("Selected(%q) = %v, want %s", tt.constraint, got, tt.want)
			}
		})
	}
	if got := Selected("~> 1.2", nil); got != nil {
		t.Errorf("no available versions: got %v", got)
	}
}

func TestBehind(t *testing.T) {
	t.Parallel()
	avail := Parse([]string{"1.2.3", "1.2.9", "1.3.0", "1.4.1", "2.0.0", "2.1.0-rc.1", "3.0.0-beta"})
	tests := []struct {
		name   string
		pinned string
		want   int
	}{
		{"1.2.3 is three lines behind", "1.2.3", 3},
		{"1.2.9 same line patch", "1.2.9", 3},
		{"older than all", "0.1.0", 4},
		{"1.3.0", "1.3.0", 2},
		{"1.4.1", "1.4.1", 1},
		{"latest", "2.0.0", 0},
		{"ahead of latest", "2.5.0", 0},
		{"prerelease of latest not counted", "2.1.0-rc.1", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Behind(mustVer(t, tt.pinned), avail); got != tt.want {
				t.Errorf("Behind(%s) = %d, want %d", tt.pinned, got, tt.want)
			}
		})
	}
	if got := Behind(nil, avail); got != 0 {
		t.Errorf("nil pinned: %d", got)
	}
	if got := Behind(mustVer(t, "1.0.0"), nil); got != 0 {
		t.Errorf("no versions: %d", got)
	}
	if got := Behind(mustVer(t, "1.0.0"), Parse([]string{"2.0.0-rc.1"})); got != 0 {
		t.Errorf("only prereleases: %d", got)
	}
	// Single-segment versions still work.
	if got := Behind(mustVer(t, "1"), Parse([]string{"1", "2", "3"})); got != 2 {
		t.Errorf("one-segment: %d", got)
	}
}

var fuzzAvail = []string{"1.0.0", "1.2.3", "1.2.9", "1.3.0", "1.4.1", "1.5.0-rc.1", "2.0.0"}

func FuzzSelectedBehind(f *testing.F) {
	for _, c := range []string{"", "1.2.3", "= 1.2.3", "~> 1.2", "~> 1.2.3", ">= 1.0, < 1.3", "< 2.0.0", "> 3.0", "~~ x", "v1.3.0", "1.2.3-rc.1"} {
		f.Add(c)
	}
	avail := Parse(fuzzAvail)
	latest := Latest(avail)
	f.Fuzz(func(t *testing.T, constraint string) {
		sel := Selected(constraint, avail)
		b := Behind(sel, avail)
		if b < 0 {
			t.Fatalf("Behind(%q) = %d < 0", constraint, b)
		}
		if sel == nil {
			if b != 0 {
				t.Fatalf("nil selection but Behind = %d", b)
			}
			return
		}
		if !latest.GreaterThan(sel) && b != 0 {
			t.Fatalf("pinned %v >= latest %v but Behind = %d", sel, latest, b)
		}
		if latest.GreaterThan(sel) && b == 0 {
			// Latest is on a newer line than sel unless only patches separate them.
			ls, ss := latest.Segments(), sel.Segments()
			if len(ls) >= 2 && len(ss) >= 2 && (ls[0] != ss[0] || ls[1] != ss[1]) {
				t.Fatalf("pinned %v is on an older line than %v but Behind = 0", sel, latest)
			}
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range fuzzAvail {
		f.Add(s)
	}
	f.Add("v1.2")
	f.Add("garbage")
	f.Fuzz(func(t *testing.T, a string) {
		out := Parse([]string{a, "1.0.0", a})
		for i := 1; i < len(out); i++ {
			if out[i].LessThan(out[i-1]) {
				t.Fatalf("not sorted: %v", strs(out))
			}
		}
		if len(out) < 1 {
			t.Fatal("lost the valid version")
		}
	})
}

func BenchmarkBehind(b *testing.B) {
	avail := Parse(fuzzAvail)
	pinned := mustVer(b, "1.2.3")
	b.ReportAllocs()
	for b.Loop() {
		_ = Behind(pinned, avail)
	}
}

func BenchmarkSelected(b *testing.B) {
	avail := Parse(fuzzAvail)
	b.ReportAllocs()
	for b.Loop() {
		_ = Selected("~> 1.2", avail)
	}
}
