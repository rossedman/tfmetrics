// Package ui renders tfmetrics' terminal output: a shared palette, and
// progress that is a live view on a terminal and plain lines everywhere else,
// so CI logs and pipes read the same as they always have.
package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

// The palette. Colors are downsampled to what the terminal supports, and
// dropped entirely when output is not a terminal or NO_COLOR is set.
var (
	colorAccent = lipgloss.Color("#7D56F4")
	colorOK     = lipgloss.Color("#04B575")
	colorWarn   = lipgloss.Color("#E5A50A")
	colorErr    = lipgloss.Color("#F25D94")
	colorSubtle = lipgloss.Color("#8A8A8A")

	// Title is for headings.
	Title = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	// Bold emphasizes a value.
	Bold = lipgloss.NewStyle().Bold(true)
	// Subtle is for secondary detail.
	Subtle = lipgloss.NewStyle().Foreground(colorSubtle)
	// OK marks success.
	OK = lipgloss.NewStyle().Foreground(colorOK)
	// Warn marks warnings.
	Warn = lipgloss.NewStyle().Foreground(colorWarn)
	// Err marks errors.
	Err = lipgloss.NewStyle().Foreground(colorErr)
)

// Icons used across commands.
const (
	IconOK     = "✓"
	IconCached = "↺"
	IconErr    = "✗"
	IconWarn   = "!"
	IconWait   = "⏸"
)

// Interactive reports whether w is a terminal that can show live progress:
// an *os.File on a TTY, with TERM not set to "dumb".
func Interactive(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd()) && os.Getenv("TERM") != "dumb"
}

// Width returns the width of the terminal w is on, or 0 when unknown.
func Width(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(f.Fd()); err == nil {
			return width
		}
	}
	return 0
}

// ScoreStyle colors a 0-100 score: green from 80, amber from 60, red below.
func ScoreStyle(score float64) lipgloss.Style {
	switch {
	case score >= 80:
		return OK
	case score >= 60:
		return Warn
	default:
		return Err
	}
}

// Line writes one line to w: styled on a live terminal, plain otherwise, so
// logs and scripts keep the plain wording.
func Line(w io.Writer, live bool, styled, plain string) error {
	if live {
		_, err := lipgloss.Fprintln(w, styled)
		return err
	}
	_, err := fmt.Fprintln(w, plain)
	return err
}

// Count formats n with thousands separators: 12,113.
func Count(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + Count(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
