package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"charm.land/lipgloss/v2"

	"github.com/rossedman/tfmetrics/internal/source"
	"github.com/rossedman/tfmetrics/internal/ui"
	"github.com/rossedman/tfmetrics/internal/usage"
)

// UsageOptions shape a who-uses report.
type UsageOptions struct {
	Target   string // what was searched for, as typed
	Provider bool   // the uses are provider requirements
	Behind   bool   // versions were looked up, so show the BEHIND column
	Width    int    // terminal width for the styled report; 0 for no limit
}

// usageColumns returns the headers and rows of a who-uses table. The address
// column appears only when the target matched more than one address.
func usageColumns(uses []usage.Use, s usage.Summary, o UsageOptions) ([]string, [][]string) {
	headers := []string{"Repo", "Owner", "Module", "Version"}
	if len(s.Addresses) > 1 {
		headers = append([]string{"Address"}, headers...)
	}
	if !o.Provider {
		headers = append(headers, "Pinned")
	}
	if o.Behind {
		headers = append(headers, "Behind")
	}
	if !o.Provider {
		headers = append(headers, "File")
	}
	rows := make([][]string, len(uses))
	for i, u := range uses {
		var row []string
		if len(s.Addresses) > 1 {
			row = append(row, addressLabel(u.Address, o.Provider))
		}
		row = append(row, u.Repo, u.Owner, u.Module, dash(u.Version))
		if !o.Provider {
			row = append(row, dash(u.Pinned))
		}
		if o.Behind {
			b := "-"
			if u.BehindKnown {
				b = strconv.Itoa(u.Behind)
			}
			row = append(row, b)
		}
		if !o.Provider {
			loc := u.File
			if loc != "" && u.Line > 0 {
				loc = fmt.Sprintf("%s:%d", u.File, u.Line)
			}
			row = append(row, dash(loc))
		}
		rows[i] = row
	}
	return headers, rows
}

// addressLabel shortens provider addresses on the default registry.
func addressLabel(address string, provider bool) string {
	if provider {
		return source.ProviderLabel(address)
	}
	return address
}

// usageHeadline says what matched and how widely it is used.
func usageHeadline(s usage.Summary, o UsageOptions) (title, counts string) {
	title = o.Target
	if len(s.Addresses) == 1 {
		title = addressLabel(s.Addresses[0], o.Provider)
	}
	noun := "call"
	if o.Provider {
		noun = "requirement"
	}
	return title, fmt.Sprintf("%s %s in %s %s", ui.Count(s.Uses), pluralize(s.Uses, noun), ui.Count(s.Repos), pluralize(s.Repos, "repo"))
}

// maxVersions caps the versions the headline lists; a popular provider can
// have a hundred distinct constraints.
const maxVersions = 8

// versionList lists the most used versions: "19.21.0 (12), none (2)", then
// how many more there are.
func versionList(s usage.Summary) string {
	var parts []string
	for i, v := range s.Versions {
		if i == maxVersions {
			parts = append(parts, fmt.Sprintf("and %d more", len(s.Versions)-maxVersions))
			break
		}
		name := v.Version
		if name == "" {
			name = "none"
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", name, v.Uses))
	}
	return strings.Join(parts, ", ")
}

// pluralize returns noun, with an "s" unless n is 1.
func pluralize(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// Usage writes the plain who-uses report: a headline, the versions in use,
// and one row per use.
func Usage(w io.Writer, uses []usage.Use, o UsageOptions) error {
	s := usage.Summarize(uses)
	title, counts := usageHeadline(s, o)
	if len(uses) == 0 {
		_, err := fmt.Fprintf(w, "%s: no uses found\n", o.Target)
		return err
	}
	fmt.Fprintf(w, "%s: %s\nversions: %s\n\n", title, counts, versionList(s))
	headers, rows := usageColumns(uses, s, o)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.ToUpper(strings.Join(headers, "\t")))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// PrettyUsage writes the styled who-uses report: the same content as Usage,
// with the behind count colored and a bordered table fitted to the terminal.
func PrettyUsage(w io.Writer, uses []usage.Use, o UsageOptions) error {
	s := usage.Summarize(uses)
	title, counts := usageHeadline(s, o)
	if len(uses) == 0 {
		_, err := lipgloss.Fprintln(w, ui.Warn.Render(ui.IconWarn+" No uses of "+o.Target+" found"))
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n%s %s\n", ui.Title.Render(title), ui.Bold.Render(counts), ui.Subtle.Render("versions"), versionList(s))
	headers, rows := usageColumns(uses, s, o)
	behindCol := -1
	for i, h := range headers {
		if h == "Behind" {
			behindCol = i
		}
	}
	b.WriteString("\n" + grid(headers, rows, o.Width, func(row, col int) (lipgloss.Style, bool) {
		if col != behindCol || row < 0 || rows[row][col] == "-" {
			return lipgloss.Style{}, false
		}
		n, _ := strconv.Atoi(rows[row][col])
		switch {
		case n == 0:
			return ui.OK, true
		case n < 3:
			return ui.Warn, true
		}
		return ui.Err, true
	}) + "\n")
	_, err := lipgloss.Fprint(w, b.String())
	return err
}

// UsageCSV writes one row per use, with a header row.
func UsageCSV(w io.Writer, uses []usage.Use) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"address", "repo", "owner", "module", "file", "line", "source_kind", "version", "pinned", "behind"})
	for _, u := range uses {
		line, behind := "", ""
		if u.Line > 0 {
			line = strconv.Itoa(u.Line)
		}
		if u.BehindKnown {
			behind = strconv.Itoa(u.Behind)
		}
		_ = cw.Write([]string{u.Address, u.Repo, u.Owner, u.Module, u.File, line, u.SourceKind, u.Version, u.Pinned, behind})
	}
	cw.Flush()
	return cw.Error()
}
