package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Status is how one item of work ended.
type Status int

// The statuses an item can end with.
const (
	Done Status = iota
	Cached
	Failed
)

// Event is one finished item, such as a scanned repo.
type Event struct {
	Name   string // what the item is, such as a repo
	Status Status
	Detail string // shown after the name on a terminal, such as "12 modules"
	Err    error
	Plain  string // the line printed in plain mode; empty prints nothing
}

// Tracker reports progress through a batch of work. It is safe to use from
// many goroutines.
type Tracker interface {
	// Done records one finished item.
	Done(e Event)
	// Wait shows that work is paused for d, such as for a rate limit, with a
	// countdown. Plain mode prints "<reason>; waiting <d>".
	Wait(reason string, d time.Duration)
	// Warn reports a warning that stays visible.
	Warn(msg string)
	// SetTotal sets how many items are expected, once that is known.
	SetTotal(n int)
	// Close stops the tracker. Print anything else only after it returns.
	Close()
}

// Options configure a tracker.
type Options struct {
	Title string // what is running, such as "Scanning acme"
	Total int    // items expected; 0 shows a spinner without a bar
	Noun  string // what an item is, such as "repos"; used in the rate
	// Items prints every finished item above the bar on a terminal; failures
	// always print.
	Items bool
	// Live shows the terminal view; otherwise items print as plain lines.
	Live bool
}

// NewTracker starts a tracker writing to w: the live view when opts.Live,
// plain lines otherwise.
func NewTracker(w io.Writer, opts Options) Tracker {
	if !opts.Live {
		return &plainTracker{w: w}
	}
	return newLiveTracker(w, opts)
}

// plainTracker prints each event's plain line, notes and warnings.
type plainTracker struct {
	mu sync.Mutex
	w  io.Writer
}

// Done prints the event's plain line.
func (t *plainTracker) Done(e Event) {
	if e.Plain == "" {
		return
	}
	t.println(e.Plain)
}

// Wait prints the reason and how long the wait is.
func (t *plainTracker) Wait(reason string, d time.Duration) {
	t.println(fmt.Sprintf("%s; waiting %s", reason, d.Round(time.Second)))
}

// Warn prints a warning.
func (t *plainTracker) Warn(msg string) { t.println("warning: " + msg) }

// SetTotal does nothing; plain output shows no bar.
func (t *plainTracker) SetTotal(int) {}

// Close does nothing; plain output has nothing to tear down.
func (t *plainTracker) Close() {}

// println writes one line under the lock.
func (t *plainTracker) println(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintln(t.w, s)
}

// liveTracker drives a Bubble Tea program that draws the live view.
type liveTracker struct {
	p       *tea.Program
	profile colorprofile.Profile
	items   bool
	done    chan struct{}
	once    sync.Once
	closed  atomic.Bool
}

// newLiveTracker starts the program in the background. It reads no input and
// leaves signals alone, so Ctrl-C cancels the command's context as usual.
func newLiveTracker(w io.Writer, opts Options) *liveTracker {
	m := newModel(opts, time.Now)
	t := &liveTracker{
		profile: colorprofile.Detect(w, os.Environ()),
		items:   opts.Items,
		done:    make(chan struct{}),
	}
	// The program is not tied to ctx: a cancelled run still reports its last
	// items, and Close is what stops the view. A program that stopped early
	// would block every later Send.
	t.p = tea.NewProgram(m,
		tea.WithColorProfile(t.profile),
		tea.WithOutput(w),
		tea.WithInput(nil),
		tea.WithoutSignalHandler(),
	)
	go func() {
		defer close(t.done)
		_, _ = t.p.Run()
	}()
	return t
}

// send passes msg to the program until the tracker is closed.
func (t *liveTracker) send(msg tea.Msg) {
	if !t.closed.Load() {
		t.p.Send(msg)
	}
}

// Done prints the event above the view when it is a failure, or when every
// item prints, then sends it to the view. Printing through the program keeps
// lines in order with everything else it is sent.
func (t *liveTracker) Done(e Event) {
	if t.closed.Load() {
		return
	}
	switch {
	case e.Status == Failed:
		t.println(failLine(e))
	case t.items:
		t.println(itemLine(e))
	}
	t.send(e)
}

// Wait sends the wait to the view.
func (t *liveTracker) Wait(reason string, d time.Duration) {
	t.send(waitMsg{reason: reason, until: time.Now().Add(d)})
}

// Warn prints the warning above the view.
func (t *liveTracker) Warn(msg string) {
	if !t.closed.Load() {
		t.println(Warn.Render(IconWarn + " " + msg))
	}
}

// println prints s above the view, with its colors reduced to what the
// terminal supports: the program does not convert printed lines itself.
func (t *liveTracker) println(s string) {
	var b strings.Builder
	_, _ = (&colorprofile.Writer{Forward: &b, Profile: t.profile}).WriteString(s)
	t.p.Println(b.String())
}

// SetTotal sends the total to the view.
func (t *liveTracker) SetTotal(n int) { t.send(totalMsg(n)) }

// Close clears the live view and waits for the program to exit.
func (t *liveTracker) Close() {
	t.once.Do(func() {
		t.p.Send(closeMsg{})
		t.closed.Store(true)
		<-t.done
	})
}

type (
	waitMsg struct {
		reason string
		until  time.Time
	}
	totalMsg int
	closeMsg struct{}
)

// model is the live view's state.
type model struct {
	opts    Options
	now     func() time.Time
	start   time.Time
	spin    spinner.Model
	bar     progress.Model
	width   int
	done    int
	cached  int
	failed  int
	last    string
	wait    waitMsg
	closing bool
}

// newModel returns the view's starting state; now is replaceable in tests.
func newModel(opts Options, now func() time.Time) model {
	return model{
		opts:  opts,
		now:   now,
		start: now(),
		spin:  spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(Title)),
		bar:   progress.New(progress.WithColors(colorAccent, colorOK), progress.WithoutPercentage(), progress.WithWidth(40)),
		width: 80,
	}
}

// Init starts the spinner.
func (m model) Init() tea.Cmd { return m.spin.Tick }

// Update applies one message.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.bar.SetWidth(max(10, min(40, msg.Width-45)))
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case Event:
		m.last = msg.Name
		switch msg.Status {
		case Failed:
			m.failed++
		case Cached:
			m.cached++
		default:
			m.done++
		}
	case waitMsg:
		m.wait = msg
	case totalMsg:
		m.opts.Total = int(msg)
	case closeMsg:
		m.closing = true
		return m, tea.Quit
	}
	return m, nil
}

// View draws the live region: a spinner and title, the bar with counts, rate
// and time left, and a countdown while work waits.
func (m model) View() tea.View {
	if m.closing {
		return tea.NewView("")
	}
	finished := m.done + m.cached + m.failed
	elapsed := m.now().Sub(m.start)
	var b strings.Builder
	b.WriteString(m.spin.View() + Title.Render(m.opts.Title))
	if m.opts.Total > 0 {
		pct := float64(finished) / float64(m.opts.Total)
		fmt.Fprintf(&b, "\n  %s %s %s",
			m.bar.ViewAs(min(pct, 1)),
			Bold.Render(fmt.Sprintf("%d/%d", finished, m.opts.Total)),
			Subtle.Render(fmt.Sprintf("%3.0f%%", pct*100)))
		if rate := rate(finished, elapsed); rate > 0 {
			left := time.Duration(float64(m.opts.Total-finished) / rate * float64(time.Second))
			fmt.Fprintf(&b, "  %s", Subtle.Render(fmt.Sprintf("%.1f %s/s · %s left", rate, m.noun(), eta(left))))
		}
	} else if finished > 0 {
		fmt.Fprintf(&b, "  %s", Bold.Render(fmt.Sprintf("%d %s", finished, m.noun())))
	}
	if finished > 0 {
		counts := []string{OK.Render(fmt.Sprintf("%s %d done", IconOK, m.done))}
		if m.cached > 0 {
			counts = append(counts, Subtle.Render(fmt.Sprintf("%s %d cached", IconCached, m.cached)))
		}
		if m.failed > 0 {
			counts = append(counts, Err.Render(fmt.Sprintf("%s %d failed", IconErr, m.failed)))
		}
		fmt.Fprintf(&b, "\n  %s", strings.Join(counts, Subtle.Render(" · ")))
		if m.last != "" {
			fmt.Fprintf(&b, "  %s", Subtle.Render(truncate("last: "+m.last, max(20, m.width-40))))
		}
	}
	if left := m.wait.until.Sub(m.now()); left > 0 {
		fmt.Fprintf(&b, "\n  %s", Warn.Render(fmt.Sprintf("%s %s · resuming in %s", IconWait, m.wait.reason, Duration(left))))
	}
	return tea.NewView(b.String())
}

// noun returns what an item is called, "items" by default.
func (m model) noun() string {
	if m.opts.Noun == "" {
		return "items"
	}
	return m.opts.Noun
}

// rate returns items per second, or 0 before there is enough to measure.
func rate(n int, elapsed time.Duration) float64 {
	if n == 0 || elapsed < time.Second {
		return 0
	}
	return float64(n) / elapsed.Seconds()
}

// eta formats the time left, rounding anything under a second to "<1s".
func eta(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	return Duration(d)
}

// itemLine renders a finished item for printing above the view.
func itemLine(e Event) string {
	icon := OK.Render(IconOK)
	if e.Status == Cached {
		icon = Subtle.Render(IconCached)
	}
	line := icon + " " + e.Name
	if e.Detail != "" {
		line += "  " + Subtle.Render(e.Detail)
	}
	return line
}

// failLine renders a failed item for printing above the view.
func failLine(e Event) string {
	line := Err.Render(IconErr + " " + e.Name)
	if e.Err != nil {
		line += "  " + Subtle.Render(firstLine(e.Err.Error()))
	}
	return line
}

// firstLine returns s up to its first newline.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// truncate shortens s to at most n cells, ending in "…" when cut.
func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// Duration formats d for people: 850ms, 12s, 3m05s, 1h02m.
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		d = d.Round(time.Minute)
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
