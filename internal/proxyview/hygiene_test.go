package proxyview_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// versionSuffix is a ".vN" import path suffix that is not part of the package name.
var versionSuffix = regexp.MustCompile(`\.v[0-9]+$`)

// importNames returns the names f's imports are referred to by.
func importNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Name != nil {
			names[imp.Name.Name] = true
			continue
		}
		p, _ := strconv.Unquote(imp.Path.Value)
		names[versionSuffix.ReplaceAllString(path.Base(p), "")] = true
	}
	return names
}

// TestNoModuleNameSelection: DD-9, kgwctl's non-test code never reads
// Claim.Module and never uses kgateway.ModuleName.
func TestNoModuleNameSelection(t *testing.T) {
	fset := token.NewFileSet()
	var scanned int
	for _, root := range []string{"../../internal", "../../pkg"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			pkgs := importNames(f)
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, isIdent := sel.X.(*ast.Ident)
				switch {
				case sel.Sel.Name == "ModuleName":
					t.Errorf("%s: uses ModuleName", fset.Position(sel.Pos()))
				case sel.Sel.Name == "Module" && (!isIdent || !pkgs[x.Name]):
					t.Errorf("%s: reads a Module field", fset.Position(sel.Pos()))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no files")
	}
}
