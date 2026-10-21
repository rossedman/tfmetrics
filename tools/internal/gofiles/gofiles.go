// Package gofiles walks a module's Go source files for the repo check tools.
package gofiles

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// File is one parsed Go source file.
type File struct {
	Path       string // path relative to the module root, slash-separated
	ImportPath string // module path + "/" + Path, as coverage profiles name it
	AST        *ast.File
	Fset       *token.FileSet
	Generated  bool
	Test       bool
}

// ModulePath reads the module path from go.mod in dir.
func ModulePath(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", sc.Err()
}

// Walk parses every .go file under the given patterns (directories, or
// dir/... for a recursive walk) relative to the module root in the current
// directory. It skips testdata, vendor and hidden directories.
func Walk(patterns []string) ([]File, error) {
	mod, err := ModulePath(".")
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	seen := map[string]bool{}
	var out []File
	for _, p := range patterns {
		root, recursive := strings.CutSuffix(p, "/...")
		if root == "" {
			root = "."
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != root && (!recursive || name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || seen[path] {
				return nil
			}
			seen[path] = true
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(filepath.Clean(path))
			out = append(out, File{
				Path:       rel,
				ImportPath: mod + "/" + rel,
				AST:        f,
				Fset:       fset,
				Generated:  ast.IsGenerated(f),
				Test:       strings.HasSuffix(path, "_test.go"),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
