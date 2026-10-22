package ui

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPlainTracker(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tr := NewTracker(&buf, Options{Title: "Scanning", Total: 3})
	var wg sync.WaitGroup
	for _, e := range []Event{
		{Name: "a", Plain: "scanned a"},
		{Name: "b", Status: Cached, Plain: "cached  b"},
		{Name: "c", Status: Failed, Err: errors.New("boom"), Plain: "error   c: boom"},
		{Name: "quiet"}, // no plain line
	} {
		wg.Go(func() { tr.Done(e) })
	}
	wg.Wait()
	tr.Wait("github api: secondary rate limit", 41500*time.Millisecond)
	tr.Warn("version lookup for x: 401")
	tr.SetTotal(9)
	tr.Close()
	got := buf.String()
	for _, want := range []string{"scanned a\n", "cached  b\n", "error   c: boom\n",
		"github api: secondary rate limit; waiting 42s\n", "warning: version lookup for x: 401\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "quiet") || strings.Count(got, "\n") != 5 {
		t.Errorf("unexpected output:\n%s", got)
	}
}

// fakeClock is a settable clock for the live view.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// view renders m without ANSI styling.
func view(m tea.Model) string { return ansi.Strip(m.View().Content) }

func TestModelProgress(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: time.Unix(1000, 0)}
	var m tea.Model = newModel(Options{Title: "Scanning 4 repos in acme", Total: 4, Noun: "repos"}, clock.now)
	if v := view(m); !strings.Contains(v, "Scanning 4 repos in acme") || !strings.Contains(v, "0/4") || strings.Contains(v, "done") {
		t.Errorf("start view:\n%s", v)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(Event{Name: "acme/a"})
	m, _ = m.Update(Event{Name: "acme/b", Status: Cached})
	clock.t = clock.t.Add(2 * time.Second)
	m, _ = m.Update(Event{Name: "acme/c", Status: Failed, Err: errors.New("boom")})
	v := view(m)
	for _, want := range []string{"3/4", "75%", "1.5 repos/s", "<1s left", "✓ 1 done", "↺ 1 cached", "✗ 1 failed", "last: acme/c"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	m, _ = m.Update(waitMsg{reason: "github api: rate limit", until: clock.t.Add(90 * time.Second)})
	if v := view(m); !strings.Contains(v, "github api: rate limit · resuming in 1m30s") {
		t.Errorf("wait not shown:\n%s", v)
	}
	clock.t = clock.t.Add(2 * time.Minute)
	if v := view(m); strings.Contains(v, "resuming") {
		t.Errorf("finished wait still shown:\n%s", v)
	}
	m, cmd := m.Update(closeMsg{})
	if view(m) != "" || cmd == nil {
		t.Errorf("close: view %q, cmd %v", view(m), cmd)
	}
}

func TestModelWithoutTotal(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{t: time.Unix(0, 0)}
	var m tea.Model = newModel(Options{Title: "Looking up module versions"}, clock.now)
	if cmd := m.Init(); cmd == nil {
		t.Error("Init should start the spinner")
	}
	m, _ = m.Update(Event{})
	if v := view(m); !strings.Contains(v, "1 items") || strings.Contains(v, "%") {
		t.Errorf("spinner view:\n%s", v)
	}
	m, _ = m.Update(totalMsg(10))
	if v := view(m); !strings.Contains(v, "1/10") {
		t.Errorf("total not applied:\n%s", v)
	}
	if _, cmd := m.Update(m.(model).spin.Tick()); cmd == nil {
		t.Error("spinner tick should schedule the next tick")
	}
}

func TestLiveTracker(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	tr := NewTracker(&buf, Options{Title: "Scanning", Total: 3, Items: true, Live: true})
	tr.Done(Event{Name: "acme/a", Detail: "12 modules"})
	tr.Done(Event{Name: "acme/b", Status: Cached})
	tr.Done(Event{Name: "acme/c", Status: Failed, Err: errors.New("clone failed\ndetail")})
	tr.Warn("registry down")
	tr.Wait("github api: rate limit", time.Second)
	tr.SetTotal(4)
	tr.Close()
	tr.Close() // idempotent
	tr.Done(Event{Name: "late"})
	tr.Warn("late")
	got := ansi.Strip(buf.String())
	for _, want := range []string{"✓ acme/a  12 modules", "↺ acme/b", "✗ acme/c  clone failed", "! registry down"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "detail") || strings.Contains(got, "late") {
		t.Errorf("unexpected output:\n%q", got)
	}
}

// syncBuffer is a bytes.Buffer safe for the program's writer goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		850 * time.Millisecond: "850ms", 12 * time.Second: "12s", 185 * time.Second: "3m05s", 62 * time.Minute: "1h02m",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
	if eta(300*time.Millisecond) != "<1s" || eta(5*time.Second) != "5s" {
		t.Error("eta")
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 12113: "12,113", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := Count(n); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
	if got := truncate("last: acme/very-long-repository-name", 16); got != "last: acme/very…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 16); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	if rate(0, time.Minute) != 0 || rate(5, 500*time.Millisecond) != 0 || rate(10, 2*time.Second) != 5 {
		t.Error("rate")
	}
	if ScoreStyle(95).GetForeground() != colorOK || ScoreStyle(70).GetForeground() != colorWarn || ScoreStyle(10).GetForeground() != colorErr {
		t.Error("ScoreStyle")
	}
	if (model{}).noun() != "items" {
		t.Error("default noun")
	}
}

func TestTerminalDetection(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if Interactive(&buf) || Width(&buf) != 0 {
		t.Error("a buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Interactive(f) || Width(f) != 0 {
		t.Error("a regular file is not a terminal")
	}
}

func TestLine(t *testing.T) {
	t.Parallel()
	var plain, styled bytes.Buffer
	if err := Line(&plain, false, OK.Render("✓ done"), "done"); err != nil || plain.String() != "done\n" {
		t.Errorf("plain = %q, %v", plain.String(), err)
	}
	// lipgloss drops styling for a writer that is not a terminal.
	if err := Line(&styled, true, OK.Render("✓ done"), "done"); err != nil || styled.String() != "✓ done\n" {
		t.Errorf("styled = %q, %v", styled.String(), err)
	}
}
