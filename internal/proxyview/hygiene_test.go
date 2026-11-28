package proxyview_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// versionSuffix is a ".vN" import path suffix that is not part of the package name.
var versionSuffix = regexp.MustCompile(`\.v[0-9]+$`)

// dotImportBanned reports whether a dot-import of p would hide a Module or ModuleName selector.
func dotImportBanned(p string) bool {
	return p == "github.com/DuncanDoyle/xdsview/pkg/envoy/provenance" || strings.HasPrefix(p, "github.com/DuncanDoyle/xdsview/pkg/adapter/")
}

// moduleNameUses returns, as "file:line:col: what", each read of a Module field,
// each ModuleName selector and each dot-import of xdsview provenance or an adapter in f (DD-9).
func moduleNameUses(fset *token.FileSet, f *ast.File) []string {
	var out []string
	pkgs := map[string]bool{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case imp.Name != nil && imp.Name.Name == "." && dotImportBanned(p):
			out = append(out, fset.Position(imp.Pos()).String()+": dot-imports "+p)
		case imp.Name != nil:
			pkgs[imp.Name.Name] = true
		default:
			pkgs[versionSuffix.ReplaceAllString(path.Base(p), "")] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, isIdent := sel.X.(*ast.Ident)
		switch {
		case sel.Sel.Name == "ModuleName":
			out = append(out, fset.Position(sel.Pos()).String()+": uses ModuleName")
		case sel.Sel.Name == "Module" && (!isIdent || !pkgs[x.Name]):
			out = append(out, fset.Position(sel.Pos()).String()+": reads a Module field")
		}
		return true
	})
	return out
}

// TestModuleNameUsesMatcher: the matcher reports a Claim.Module read, a
// ModuleName use and a dot-import, and passes a package's Module type.
func TestModuleNameUsesMatcher(t *testing.T) {
	const src = `package p

import (
	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	. "github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
)

var _ provenance.Module

func f(c provenance.Claim) bool { return c.Module == kgateway.ModuleName }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := moduleNameUses(fset, f)
	want := []string{"dot-imports github.com/DuncanDoyle/xdsview/pkg/envoy/provenance", "reads a Module field", "uses ModuleName"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want one each of %q", got, want)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], "snippet.go:") || !strings.HasSuffix(got[i], ": "+w) {
			t.Errorf("finding %d = %q, want snippet.go:<line>:<col>: %s", i, got[i], w)
		}
	}
}

// skipDir reports whether the scan skips a directory: examples, testdata, vendor and hidden ones.
func skipDir(name string) bool {
	return slices.Contains([]string{"examples", "testdata", "vendor"}, name) || strings.HasPrefix(name, ".")
}

// TestNoModuleNameSelection: DD-9, kgwctl's non-test code (every root but
// examples/) never reads Claim.Module and never uses kgateway.ModuleName.
func TestNoModuleNameSelection(t *testing.T) {
	const root = "../.."
	fset := token.NewFileSet()
	roots := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != root && skipDir(d.Name()):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		roots[strings.Split(filepath.ToSlash(rel), "/")[0]] = true
		for _, u := range moduleNameUses(fset, f) {
			t.Error(u)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"cmd", "internal", "pkg"} {
		if !roots[r] {
			t.Errorf("scanned no file under %s/", r)
		}
	}
}
