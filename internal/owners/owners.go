// Package owners resolves who owns a repo: CODEOWNERS first, then an owner
// custom property, then a team-<name> topic, else "unowned".
package owners

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Unowned is the owner reported when nothing else matches.
const Unowned = "unowned"

// codeownersPaths are the locations GitHub reads CODEOWNERS from, in order.
var codeownersPaths = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// Resolve returns the owners of the repo checked out at root.
func Resolve(root string, properties map[string]string, ownerProperty string, topics []string) []string {
	if o := FromCodeowners(root); len(o) > 0 {
		return o
	}
	if ownerProperty != "" {
		if v := strings.TrimSpace(properties[ownerProperty]); v != "" {
			return []string{v}
		}
	}
	var fromTopics []string
	for _, t := range topics {
		if name, ok := strings.CutPrefix(t, "team-"); ok && name != "" {
			fromTopics = append(fromTopics, name)
		}
	}
	if len(fromTopics) > 0 {
		sort.Strings(fromTopics)
		return fromTopics
	}
	return []string{Unowned}
}

// FromCodeowners returns the owners of the catch-all rule in the repo's
// CODEOWNERS file, or the owners of every rule when there is no catch-all.
// Leading @ and org/ prefixes on team handles are kept as written, minus the @.
func FromCodeowners(root string) []string {
	for _, p := range codeownersPaths {
		f, err := os.Open(filepath.Join(root, p))
		if err != nil {
			continue
		}
		defer func() { _ = f.Close() }()
		return parseCodeowners(bufio.NewScanner(f))
	}
	return nil
}

// parseCodeowners does the work of FromCodeowners on an open file.
func parseCodeowners(sc *bufio.Scanner) []string {
	var catchAll []string
	all := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		var owners []string
		for _, o := range fields[1:] {
			o = strings.TrimLeft(o, "@")
			if o != "" {
				owners = append(owners, o)
				all[o] = true
			}
		}
		if fields[0] == "*" || fields[0] == "/*" || fields[0] == "/**" {
			catchAll = owners // the last matching rule wins in CODEOWNERS
		}
	}
	if len(catchAll) > 0 {
		sort.Strings(catchAll)
		return catchAll
	}
	out := make([]string, 0, len(all))
	for o := range all {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}
