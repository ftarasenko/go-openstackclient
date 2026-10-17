package openstack_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoPackageState holds every package under pkg/ to the rule no linter
// expresses: no mutable package-level state. A long-running importer builds
// many sessions, for many regions and credentials, in one process; a package
// variable is shared by all of them, as the CLI's name→ID memo once was.
//
// A package-level var is allowed only when it is immutable in practice: an
// error sentinel (errors.New), a compiled regexp (regexp.MustCompile), or a
// blank compile-time interface check (var _ I = T{}). Everything else —
// caches, clocks, hooks, defaults a caller could overwrite — belongs on a
// value the caller owns.
func TestNoPackageState(t *testing.T) {
	root := filepath.Join("..") // pkg/
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !immutableVar(name, vs, i) {
						t.Errorf("%s: package-level var %s is mutable state; pkg/ may hold none (see TestNoPackageState)",
							fset.Position(name.Pos()), name.Name)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func immutableVar(name *ast.Ident, vs *ast.ValueSpec, i int) bool {
	if name.Name == "_" {
		return true
	}
	if i >= len(vs.Values) {
		return false
	}
	call, ok := vs.Values[i].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch pkg.Name + "." + sel.Sel.Name {
	case "errors.New", "regexp.MustCompile":
		return true
	}
	return false
}
