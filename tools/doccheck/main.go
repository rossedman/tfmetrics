// Command doccheck fails when a function, method or package lacks a doc
// comment. Test files and generated files are skipped.
package main

import (
	"fmt"
	"go/ast"
	"os"
	"path"
	"sort"

	"github.com/rossedman/tfmetrics/tools/internal/gofiles"
)

// main walks the packages named on the command line and reports gaps.
func main() {
	files, err := gofiles.Walk(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(2)
	}
	problems := check(files)
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "doccheck: %d missing doc comments\n", len(problems))
		os.Exit(1)
	}
}

// check returns one line per missing doc comment.
func check(files []gofiles.File) []string {
	var out []string
	pkgDoc := map[string]bool{}
	pkgSeen := map[string]bool{}
	for _, f := range files {
		if f.Test || f.Generated {
			continue
		}
		dir := path.Dir(f.Path)
		pkgSeen[dir] = true
		if f.AST.Doc != nil {
			pkgDoc[dir] = true
		}
		for _, d := range f.AST.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Doc != nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				name = recvName(fn.Recv.List[0].Type) + "." + name
			}
			pos := f.Fset.Position(fn.Pos())
			out = append(out, fmt.Sprintf("%s:%d: %s has no doc comment", f.Path, pos.Line, name))
		}
	}
	for dir := range pkgSeen {
		if !pkgDoc[dir] {
			out = append(out, fmt.Sprintf("%s: package has no package comment", dir))
		}
	}
	sort.Strings(out)
	return out
}

// recvName returns the type name of a method receiver.
func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}
