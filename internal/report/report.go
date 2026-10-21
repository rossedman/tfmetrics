// Package report renders a human-readable summary of one repo's facts, as
// plain text for the terminal or Markdown for a PR comment or job summary.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Summary is the per-repo numbers both formats show.
type Summary struct {
	Roots, Children, Resources, DataSources, Calls, Unpinned, Lines, ParseErrors int
	Hygiene                                                                      metrics.HygieneResult
}

// Summarize counts the headline numbers for a repo.
func Summarize(r facts.ScanResult, weights map[string]float64) Summary {
	s := Summary{Hygiene: metrics.Hygiene(r, weights)}
	for _, m := range r.Modules {
		if m.Kind == facts.KindRoot {
			s.Roots++
		} else {
			s.Children++
		}
		s.Resources += len(m.Resources)
		s.DataSources += len(m.DataSources)
		s.Lines += m.Lines
		s.ParseErrors += m.ParseErrorFiles()
		for _, c := range m.ModuleCalls {
			s.Calls++
			if c.SourceKind != source.KindLocal && c.Pinned == facts.PinnedNone {
				s.Unpinned++
			}
		}
	}
	return s
}

// checkNames are human names for hygiene checks.
var checkNames = map[string]string{
	"pinned":               "Remote module calls pinned",
	"required_version":     "Roots with required_version",
	"provider_constraints": "Providers with a version constraint",
	"typed_vars":           "Variables with a type",
	"described":            "Variables and outputs described",
	"parse_clean":          "Modules that parse cleanly",
}

// Text writes the terminal report for one repo.
func Text(w io.Writer, r facts.ScanResult, weights map[string]float64) error {
	s := Summarize(r, weights)
	commit := r.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit == "" {
		commit = "(not a git checkout)"
	}
	fmt.Fprintf(w, "%s @ %s\n", r.Repo, commit)
	fmt.Fprintf(w, "owners: %s\n\n", metrics.Owner(r))
	fmt.Fprintf(w, "modules      %d (%d root, %d child)\n", s.Roots+s.Children, s.Roots, s.Children)
	fmt.Fprintf(w, "resources    %d declared, %d data sources\n", s.Resources, s.DataSources)
	fmt.Fprintf(w, "module calls %d (%d remote unpinned)\n", s.Calls, s.Unpinned)
	fmt.Fprintf(w, "lines        %d\n", s.Lines)
	fmt.Fprintf(w, "parse errors %d\n\n", s.ParseErrors)

	fmt.Fprintf(w, "hygiene score %.1f / 100\n", s.Hygiene.Score)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range s.Hygiene.Checks {
		fmt.Fprintf(tw, "  %s\t%d/%d\tweight %g\n", checkNames[c.Key], c.Passed, c.Total, c.Weight)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(r.Modules) > 0 {
		fmt.Fprintln(w, "\nmodules")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  PATH\tKIND\tRESOURCES\tCALLS\tBACKEND\tREQUIRED_VERSION")
		for _, m := range r.Modules {
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%s\t%s\n", m.Path, m.Kind, len(m.Resources), len(m.ModuleCalls), dash(m.Backend), dash(m.RequiredVersion))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	calls := remoteCalls(r)
	if len(calls) > 0 {
		fmt.Fprintln(w, "\nremote module calls")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  ADDRESS\tKIND\tVERSION\tPINNED\tIN")
		for _, c := range calls {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", c.call.Address, c.call.SourceKind, dash(c.call.Version), c.call.Pinned, c.path)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	diags := diagnostics(r)
	if len(diags) > 0 {
		fmt.Fprintln(w, "\ndiagnostics")
		for _, d := range diags {
			fmt.Fprintf(w, "  %s %s: %s\n", d.Severity, location(d), d.Summary)
		}
	}
	return nil
}

// Markdown writes the Markdown report for one repo.
func Markdown(w io.Writer, r facts.ScanResult, weights map[string]float64) error {
	s := Summarize(r, weights)
	fmt.Fprintf(w, "### tfmetrics: %s\n\n", r.Repo)
	fmt.Fprintf(w, "**Hygiene score: %.1f / 100** · %d modules (%d root, %d child) · %d resources · %d module calls (%d unpinned) · %d parse errors\n\n",
		s.Hygiene.Score, s.Roots+s.Children, s.Roots, s.Children, s.Resources, s.Calls, s.Unpinned, s.ParseErrors)
	fmt.Fprintln(w, "| Check | Passed | Weight |")
	fmt.Fprintln(w, "| --- | --- | --- |")
	for _, c := range s.Hygiene.Checks {
		fmt.Fprintf(w, "| %s | %d/%d | %g |\n", checkNames[c.Key], c.Passed, c.Total, c.Weight)
	}
	calls := remoteCalls(r)
	if len(calls) > 0 {
		fmt.Fprintln(w, "\n<details><summary>Remote module calls</summary>")
		fmt.Fprintln(w, "\n| Address | Version | Pinned | In |")
		fmt.Fprintln(w, "| --- | --- | --- | --- |")
		for _, c := range calls {
			fmt.Fprintf(w, "| `%s` | %s | %s | `%s` |\n", c.call.Address, mdDash(c.call.Version), c.call.Pinned, c.path)
		}
		fmt.Fprintln(w, "\n</details>")
	}
	diags := diagnostics(r)
	if len(diags) > 0 {
		fmt.Fprintln(w, "\n<details><summary>Diagnostics</summary>")
		fmt.Fprintln(w)
		for _, d := range diags {
			fmt.Fprintf(w, "- **%s** `%s`: %s\n", d.Severity, location(d), d.Summary)
		}
		fmt.Fprintln(w, "\n</details>")
	}
	_, err := fmt.Fprintln(w)
	return err
}

// locatedCall is a module call and the module it is in.
type locatedCall struct {
	path string
	call facts.ModuleCall
}

// remoteCalls returns non-local module calls sorted by address.
func remoteCalls(r facts.ScanResult) []locatedCall {
	var out []locatedCall
	for _, m := range r.Modules {
		for _, c := range m.ModuleCalls {
			if c.SourceKind != source.KindLocal {
				out = append(out, locatedCall{m.Path, c})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].call.Address < out[j].call.Address })
	return out
}

// diagnostics returns every diagnostic in the repo.
func diagnostics(r facts.ScanResult) []facts.Diagnostic {
	var out []facts.Diagnostic
	for _, m := range r.Modules {
		out = append(out, m.Diagnostics...)
	}
	return out
}

// location formats a diagnostic's file and line.
func location(d facts.Diagnostic) string {
	if d.File == "" {
		return "-"
	}
	if d.Line > 0 {
		return fmt.Sprintf("%s:%d", d.File, d.Line)
	}
	return d.File
}

// dash returns s, or "-" when empty.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// mdDash returns s in backticks, or an em dash when empty.
func mdDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return "`" + s + "`"
}
