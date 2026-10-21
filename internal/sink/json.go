package sink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

// JSON writes metrics.json and a copy of every facts file under facts/, so one
// directory holds a complete, archivable run.
type JSON struct {
	Dir string
}

// Emit writes the files.
func (j *JSON) Emit(_ context.Context, results []facts.ScanResult, m metrics.Metrics) error {
	if err := checkLocal(j.Dir); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := facts.Write(filepath.Join(j.Dir, "facts"), r); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.Dir, "metrics.json"), append(b, '\n'), 0o644)
}
