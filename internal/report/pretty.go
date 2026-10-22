package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/ui"
	"github.com/rossedman/tfmetrics/internal/versions"
)

// Pretty writes the styled terminal report for one repo: the same content as
// Text, with a score bar, colored checks and bordered tables fitted to
// o.Width cells. Colors are dropped when w is not a terminal.
func Pretty(w io.Writer, r facts.ScanResult, o Options) error {
	s := Summarize(r, o.Weights)
	width := o.Width
	calls := remoteCalls(r)
	var b strings.Builder

	commit := r.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		commit = "not a git checkout"
	}
	fmt.Fprintf(&b, "%s %s\n", ui.Title.Render(r.Repo), ui.Subtle.Render("@ "+commit))
	fmt.Fprintf(&b, "%s %s\n\n", ui.Subtle.Render("owners"), metrics.Owner(r))

	stats := []string{
		stat("modules", s.Roots+s.Children, fmt.Sprintf("%d root · %d child", s.Roots, s.Children)),
		stat("resources", s.Resources, dataSources(s.DataSources)),
		stat("module calls", s.Calls, fmt.Sprintf("%d remote unpinned", s.Unpinned)),
	}
	if o.Gaps != nil {
		u := updates(calls, o.Gaps)
		stats = append(stats, stat("outdated", u.Outdated(), fmt.Sprintf("%d major · %d minor · %d patch", u.Major, u.Minor, u.Patch)))
	}
	stats = append(stats,
		stat("lines", s.Lines, ""),
		stat("parse errors", s.ParseErrors, ""),
	)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, stats...) + "\n\n")

	score := ui.ScoreStyle(s.Hygiene.Score)
	fmt.Fprintf(&b, "%s  %s  %s\n", ui.Bold.Render("Hygiene"), bar(s.Hygiene.Score/100, 30),
		score.Bold(true).Render(fmt.Sprintf("%.1f", s.Hygiene.Score))+ui.Subtle.Render(" / 100"))
	checks := table.New().Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).BorderColumn(false).BorderHeader(false).
		StyleFunc(func(_, col int) lipgloss.Style {
			style := lipgloss.NewStyle().PaddingRight(2)
			if col == 0 {
				style = style.PaddingLeft(2)
			}
			if col == 3 {
				return style.Inherit(ui.Subtle)
			}
			return style
		})
	for _, c := range s.Hygiene.Checks {
		ratio := 1.0
		if c.Total > 0 {
			ratio = float64(c.Passed) / float64(c.Total)
		}
		checks.Row(checkIcon(ratio)+" "+checkNames[c.Key], fmt.Sprintf("%d/%d", c.Passed, c.Total), bar(ratio, 12), fmt.Sprintf("weight %g", c.Weight))
	}
	b.WriteString(checks.Render() + "\n")

	if len(r.Modules) > 0 {
		rows := make([][]string, len(r.Modules))
		for i, m := range r.Modules {
			rows[i] = []string{m.Path, m.Kind, strconv.Itoa(len(m.Resources)), strconv.Itoa(len(m.ModuleCalls)), dash(m.Backend), dash(m.RequiredVersion)}
		}
		b.WriteString(section("Modules", grid([]string{"Path", "Kind", "Resources", "Calls", "Backend", "Required version"}, rows, width, nil)))
	}

	if len(calls) > 0 {
		headers := []string{"Address", "Kind", "Version", "Pinned", "In"}
		if o.Gaps != nil {
			headers = []string{"Address", "Kind", "Version", "Pinned", "Latest", "Behind", "In"}
		}
		rows := make([][]string, len(calls))
		kinds := make([]string, len(calls))
		for i, c := range calls {
			if o.Gaps != nil {
				rows[i] = []string{c.call.Address, c.call.SourceKind, dash(c.call.Version), c.call.Pinned, dash(latest(o.Gaps, c)), behind(o.Gaps, c), c.path}
				if g, ok := o.Gaps[GapKey(c.path, c.call.Name)]; ok {
					kinds[i] = g.Update()
				}
			} else {
				rows[i] = []string{c.call.Address, c.call.SourceKind, dash(c.call.Version), c.call.Pinned, c.path}
			}
		}
		style := func(row, col int) (lipgloss.Style, bool) {
			if row < 0 {
				return lipgloss.Style{}, false
			}
			switch {
			case col == 3:
				switch rows[row][3] {
				case facts.PinnedExact:
					return ui.OK, true
				case facts.PinnedRange:
					return ui.Warn, true
				}
				return ui.Err, true
			case col == 5 && o.Gaps != nil:
				return updateStyle(kinds[row])
			}
			return lipgloss.Style{}, false
		}
		b.WriteString(section("Remote module calls", grid(headers, rows, width, style)))
	}

	if diags := diagnostics(r); len(diags) > 0 {
		var lines []string
		for _, d := range diags {
			icon := ui.Warn.Render(ui.IconWarn)
			if d.Severity == facts.SeverityError {
				icon = ui.Err.Render(ui.IconErr)
			}
			lines = append(lines, fmt.Sprintf("%s %s  %s", icon, d.Summary, ui.Subtle.Render(location(d))))
		}
		b.WriteString(section("Diagnostics", strings.Join(lines, "\n")))
	}
	_, err := lipgloss.Fprint(w, b.String())
	return err
}

// updateStyle colors a call's Behind cell by the biggest update it is
// missing: major in red, minor in yellow, up to date in green.
func updateStyle(kind string) (lipgloss.Style, bool) {
	switch kind {
	case versions.UpdateMajor:
		return ui.Err, true
	case versions.UpdateMinor:
		return ui.Warn, true
	case versions.UpdateCurrent:
		return ui.OK, true
	}
	return lipgloss.Style{}, false
}

// dataSources counts data sources: "1 data source", "162 data sources".
func dataSources(n int) string {
	if n == 1 {
		return "1 data source"
	}
	return ui.Count(n) + " data sources"
}

// stat renders one headline number with its label and a detail line.
func stat(label string, n int, detail string) string {
	block := ui.Subtle.Render(label) + "\n" + ui.Bold.Render(ui.Count(n))
	if detail != "" {
		block += " " + ui.Subtle.Render("("+detail+")")
	}
	return lipgloss.NewStyle().PaddingRight(4).Render(block)
}

// bar renders a fraction from 0 to 1 as a bar width cells wide.
func bar(frac float64, width int) string {
	// Full blocks: the default half blocks need a background color behind
	// them, which a static report does not paint.
	p := progress.New(progress.WithColors(ui.ScoreStyle(frac*100).GetForeground()), progress.WithoutPercentage(),
		progress.WithWidth(width), progress.WithFillCharacters('█', '░'))
	return p.ViewAs(frac)
}

// checkIcon marks a hygiene check as passed, partly passed or failed.
func checkIcon(ratio float64) string {
	switch {
	case ratio >= 1:
		return ui.OK.Render(ui.IconOK)
	case ratio > 0:
		return ui.Warn.Render(ui.IconWarn)
	default:
		return ui.Err.Render(ui.IconErr)
	}
}

// section renders a heading above body.
func section(title, body string) string {
	return "\n" + ui.Bold.Render(title) + "\n" + body + "\n"
}

// grid renders rows as a table with a rounded border and a bold header, at
// most width cells wide when width > 0: the widest columns give way first and
// their cells wrap, so short columns stay readable. cell, when set, styles
// individual cells; it returns false to keep the default.
func grid(headers []string, rows [][]string, width int, cell func(row, col int) (lipgloss.Style, bool)) string {
	widths := fitColumns(headers, rows, width)
	return table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(ui.Subtle).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			base := lipgloss.NewStyle().Padding(0, 1).Width(widths[col] + 2)
			if row == table.HeaderRow {
				return base.Inherit(ui.Title)
			}
			if cell != nil {
				if s, ok := cell(row, col); ok {
					return base.Inherit(s)
				}
			}
			return base
		}).
		Render()
}

// fitColumns returns each column's content width: its widest cell, narrowed
// widest-first until the table fits in width (0 for no limit). A column is
// never narrowed below its header or 8 cells.
func fitColumns(headers []string, rows [][]string, width int) []int {
	widths := make([]int, len(headers))
	floor := make([]int, len(headers))
	for c, h := range headers {
		widths[c] = lipgloss.Width(h)
		floor[c] = max(widths[c], 8)
		for _, r := range rows {
			widths[c] = max(widths[c], lipgloss.Width(r[c]))
		}
		floor[c] = min(floor[c], widths[c])
	}
	if width <= 0 {
		return widths
	}
	// Each column adds a border and one cell of padding either side.
	budget := width - 1 - 3*len(headers)
	for sum(widths) > budget {
		widest := 0
		for c := range widths {
			if widths[c]-floor[c] > widths[widest]-floor[widest] {
				widest = c
			}
		}
		if widths[widest] <= floor[widest] {
			break // as narrow as it goes
		}
		widths[widest]--
	}
	return widths
}

// sum adds up ns.
func sum(ns []int) int {
	t := 0
	for _, n := range ns {
		t += n
	}
	return t
}
