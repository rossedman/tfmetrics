// Package versions works out how far a module call lags behind the latest
// release, from its version constraint or git ref and the available versions.
package versions

import (
	"sort"
	"strings"

	goversion "github.com/hashicorp/go-version"
)

// Parse parses the given version strings, dropping any that are not valid
// versions (such as non-semver git tags), and returns them sorted ascending.
func Parse(raw []string) []*goversion.Version {
	out := make([]*goversion.Version, 0, len(raw))
	for _, r := range raw {
		v, err := goversion.NewVersion(strings.TrimSpace(r))
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	sort.Sort(goversion.Collection(out))
	return out
}

// Latest returns the highest release (non-prerelease) version, or nil.
func Latest(available []*goversion.Version) *goversion.Version {
	for i := len(available) - 1; i >= 0; i-- {
		if available[i].Prerelease() == "" {
			return available[i]
		}
	}
	return nil
}

// Selected returns the version a constraint or ref resolves to: the version
// itself when it is exact, otherwise the highest release that satisfies the
// constraint (what terraform init would pick). It returns nil when the
// constraint is empty, invalid, or matches nothing.
func Selected(constraint string, available []*goversion.Version) *goversion.Version {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	if v, err := goversion.NewVersion(strings.TrimPrefix(constraint, "=")); err == nil {
		return v
	}
	cs, err := goversion.NewConstraint(constraint)
	if err != nil {
		return nil
	}
	for i := len(available) - 1; i >= 0; i-- {
		v := available[i]
		if v.Prerelease() == "" && cs.Check(v) {
			return v
		}
	}
	return nil
}

// Behind counts the distinct major.minor release lines that are newer than
// pinned, up to and including latest. 1.2.3 against 1.2.9, 1.3.0, 1.4.1 and
// 2.0.0 is 3 behind. Patch releases on the same line do not count.
func Behind(pinned *goversion.Version, available []*goversion.Version) int {
	if pinned == nil {
		return 0
	}
	latest := Latest(available)
	if latest == nil || !latest.GreaterThan(pinned) {
		return 0
	}
	pm := line(pinned)
	lines := map[[2]int]bool{}
	for _, v := range available {
		if v.Prerelease() != "" || v.GreaterThan(latest) {
			continue
		}
		l := line(v)
		if l[0] > pm[0] || (l[0] == pm[0] && l[1] > pm[1]) {
			lines[l] = true
		}
	}
	return len(lines)
}

// line returns a version's major and minor numbers.
func line(v *goversion.Version) [2]int {
	s := v.Segments()
	out := [2]int{}
	for i := 0; i < 2 && i < len(s); i++ {
		out[i] = s[i]
	}
	return out
}

// Update kinds: the biggest kind of release a call is missing.
const (
	UpdateMajor   = "major"
	UpdateMinor   = "minor"
	UpdatePatch   = "patch"
	UpdateCurrent = "current"
)

// Gap is how far a version trails the latest release, counted in releases.
// Each newer release counts as major, minor or patch by what it changes from
// the release before it, so Major+Minor equals Behind.
type Gap struct {
	Latest              *goversion.Version
	Major, Minor, Patch int
}

// Releases returns how many releases are newer than the compared version.
func (g Gap) Releases() int {
	return g.Major + g.Minor + g.Patch
}

// Update returns the biggest kind of release the compared version is missing:
// UpdateMajor, UpdateMinor, UpdatePatch or UpdateCurrent.
func (g Gap) Update() string {
	switch {
	case g.Major > 0:
		return UpdateMajor
	case g.Minor > 0:
		return UpdateMinor
	case g.Patch > 0:
		return UpdatePatch
	}
	return UpdateCurrent
}

// Compare counts the releases (not prereleases) newer than pinned, up to and
// including the latest, by kind. 1.2.3 against 1.2.4, 1.2.5, 1.3.0 and 2.0.0
// is 1 major, 1 minor and 2 patch releases behind.
func Compare(pinned *goversion.Version, available []*goversion.Version) Gap {
	g := Gap{Latest: Latest(available)}
	if pinned == nil || g.Latest == nil || !g.Latest.GreaterThan(pinned) {
		return g
	}
	prev := pinned
	for _, v := range available {
		if v.Prerelease() != "" || !v.GreaterThan(pinned) || v.GreaterThan(g.Latest) {
			continue
		}
		p, l := line(prev), line(v)
		switch {
		case l[0] != p[0]:
			g.Major++
		case l[1] != p[1]:
			g.Minor++
		default:
			g.Patch++
		}
		prev = v
	}
	return g
}
