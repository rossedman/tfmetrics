package sink

import (
	"context"
	"io"
	"os"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/report"
)

// Markdown writes a per-repo summary, for a PR comment or a job summary.
type Markdown struct {
	Path        string // file, or "-" for stdout
	StepSummary bool   // also append to $GITHUB_STEP_SUMMARY when set
	Weights     map[string]float64
	Stdout      io.Writer
}

// Emit writes one section per repo.
func (m *Markdown) Emit(_ context.Context, results []facts.ScanResult, _ metrics.Metrics) error {
	var w io.Writer = m.Stdout
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	if m.Path != "-" && m.Path != "" {
		if err := checkLocal(m.Path); err != nil {
			return err
		}
		f, err := os.Create(m.Path)
		if err != nil {
			return err
		}
		files = append(files, f)
		w = f
	}
	writers := []io.Writer{w}
	if p := os.Getenv("GITHUB_STEP_SUMMARY"); m.StepSummary && p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		files = append(files, f)
		writers = append(writers, f)
	}
	out := io.MultiWriter(writers...)
	for _, r := range results {
		if err := report.Markdown(out, r, m.Weights); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}
