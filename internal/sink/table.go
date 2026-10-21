package sink

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

// ModuleRow is one row per module directory.
type ModuleRow struct {
	Repo                string    `parquet:"repo" csv:"repo"`
	Org                 string    `parquet:"org" csv:"org"`
	Owner               string    `parquet:"owner" csv:"owner"`
	Commit              string    `parquet:"commit" csv:"commit"`
	ScannedAt           time.Time `parquet:"scanned_at,timestamp(millisecond)" csv:"scanned_at"`
	Path                string    `parquet:"path" csv:"path"`
	Kind                string    `parquet:"kind" csv:"kind"`
	Files               int64     `parquet:"files" csv:"files"`
	Lines               int64     `parquet:"lines" csv:"lines"`
	RequiredVersion     string    `parquet:"required_version" csv:"required_version"`
	Backend             string    `parquet:"backend" csv:"backend"`
	Providers           int64     `parquet:"providers" csv:"providers"`
	ProvidersConstraint int64     `parquet:"providers_constrained" csv:"providers_constrained"`
	Resources           int64     `parquet:"resources" csv:"resources"`
	DataSources         int64     `parquet:"data_sources" csv:"data_sources"`
	ModuleCalls         int64     `parquet:"module_calls" csv:"module_calls"`
	Variables           int64     `parquet:"variables" csv:"variables"`
	VariablesUntyped    int64     `parquet:"variables_untyped" csv:"variables_untyped"`
	VariablesUndescr    int64     `parquet:"variables_undescribed" csv:"variables_undescribed"`
	Outputs             int64     `parquet:"outputs" csv:"outputs"`
	OutputsUndescribed  int64     `parquet:"outputs_undescribed" csv:"outputs_undescribed"`
	PreventDestroy      int64     `parquet:"prevent_destroy" csv:"prevent_destroy"`
	MovedBlocks         int64     `parquet:"moved_blocks" csv:"moved_blocks"`
	ImportBlocks        int64     `parquet:"import_blocks" csv:"import_blocks"`
	ParseErrors         int64     `parquet:"parse_errors" csv:"parse_errors"`
}

// CallRow is one row per module call.
type CallRow struct {
	Repo       string    `parquet:"repo" csv:"repo"`
	Org        string    `parquet:"org" csv:"org"`
	Owner      string    `parquet:"owner" csv:"owner"`
	Commit     string    `parquet:"commit" csv:"commit"`
	ScannedAt  time.Time `parquet:"scanned_at,timestamp(millisecond)" csv:"scanned_at"`
	ModulePath string    `parquet:"module_path" csv:"module_path"`
	Name       string    `parquet:"name" csv:"name"`
	Source     string    `parquet:"source" csv:"source"`
	SourceKind string    `parquet:"source_kind" csv:"source_kind"`
	Address    string    `parquet:"address" csv:"address"`
	Version    string    `parquet:"version" csv:"version"`
	Pinned     string    `parquet:"pinned" csv:"pinned"`
	Count      bool      `parquet:"count" csv:"count"`
	ForEach    bool      `parquet:"for_each" csv:"for_each"`
}

// Rows flattens results into module rows and call rows.
func Rows(results []facts.ScanResult) ([]ModuleRow, []CallRow) {
	var mods []ModuleRow
	var calls []CallRow
	for _, r := range results {
		owner := metrics.Owner(r)
		for _, m := range r.Modules {
			row := ModuleRow{
				Repo: r.Repo, Org: r.Org, Owner: owner, Commit: r.Commit, ScannedAt: r.ScannedAt,
				Path: m.Path, Kind: m.Kind, Files: int64(m.Files), Lines: int64(m.Lines),
				RequiredVersion: m.RequiredVersion, Backend: m.Backend,
				Providers: int64(len(m.Providers)), Resources: int64(len(m.Resources)),
				DataSources: int64(len(m.DataSources)), ModuleCalls: int64(len(m.ModuleCalls)),
				Variables: int64(len(m.Variables)), Outputs: int64(len(m.Outputs)),
				PreventDestroy: int64(m.Lifecycle.PreventDestroy),
				MovedBlocks:    int64(m.MovedBlocks), ImportBlocks: int64(m.ImportBlocks),
				ParseErrors: int64(m.ParseErrorFiles()),
			}
			for _, p := range m.Providers {
				if p.Constraint != "" {
					row.ProvidersConstraint++
				}
			}
			for _, v := range m.Variables {
				if !v.HasType {
					row.VariablesUntyped++
				}
				if !v.HasDescription {
					row.VariablesUndescr++
				}
			}
			for _, o := range m.Outputs {
				if !o.HasDescription {
					row.OutputsUndescribed++
				}
			}
			mods = append(mods, row)
			for _, c := range m.ModuleCalls {
				calls = append(calls, CallRow{
					Repo: r.Repo, Org: r.Org, Owner: owner, Commit: r.Commit, ScannedAt: r.ScannedAt,
					ModulePath: m.Path, Name: c.Name, Source: c.Source, SourceKind: c.SourceKind,
					Address: c.Address, Version: c.Version, Pinned: c.Pinned, Count: c.Count, ForEach: c.ForEach,
				})
			}
		}
	}
	return mods, calls
}

// Table writes modules.<ext> and module_calls.<ext> as CSV or Parquet.
type Table struct {
	Dir    string
	Format string // csv | parquet
}

// Emit writes both tables.
func (t *Table) Emit(_ context.Context, results []facts.ScanResult, _ metrics.Metrics) error {
	if err := checkLocal(t.Dir); err != nil {
		return err
	}
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return err
	}
	mods, calls := Rows(results)
	switch t.Format {
	case "csv":
		if err := writeCSV(filepath.Join(t.Dir, "modules.csv"), mods); err != nil {
			return err
		}
		return writeCSV(filepath.Join(t.Dir, "module_calls.csv"), calls)
	case "parquet":
		if err := parquet.WriteFile(filepath.Join(t.Dir, "modules.parquet"), mods); err != nil {
			return err
		}
		return parquet.WriteFile(filepath.Join(t.Dir, "module_calls.parquet"), calls)
	}
	return fmt.Errorf("unknown table format %q", t.Format)
}

// writeCSV writes rows of a struct type, with a header from the csv tags.
func writeCSV[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	typ := reflect.TypeFor[T]()
	header := make([]string, typ.NumField())
	for i := range header {
		header[i] = typ.Field(i).Tag.Get("csv")
	}
	if err := w.Write(header); err != nil {
		_ = f.Close()
		return err
	}
	for _, r := range rows {
		v := reflect.ValueOf(r)
		rec := make([]string, v.NumField())
		for i := range rec {
			rec[i] = cell(v.Field(i))
		}
		if err := w.Write(rec); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// cell formats one CSV value.
func cell(v reflect.Value) string {
	switch x := v.Interface().(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return strings.ReplaceAll(x, "\n", " ")
	}
	return fmt.Sprint(v.Interface())
}
