package view

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// jsonFields returns t's JSON field names, flattening embedded structs as encoding/json does.
func jsonFields(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			out = append(out, jsonFields(ft)...)
			continue
		}
		out = append(out, tag)
	}
	return out
}

// docFields returns the field names of each "### Type" table in the doc.
func docFields(t *testing.T, path string) map[string][]string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `([A-Za-z]+)` \\|")
	out := map[string][]string{}
	cur := ""
	for _, l := range strings.Split(string(b), "\n") {
		if name, ok := strings.CutPrefix(l, "### "); ok {
			cur = name
			continue
		}
		if m := row.FindStringSubmatch(l); m != nil && cur != "" {
			out[cur] = append(out[cur], m[1])
		}
	}
	return out
}

// TestOutputDocMatchesSchema fails when docs/output.md and the output types disagree on a field.
func TestOutputDocMatchesSchema(t *testing.T) {
	types := []any{
		DescribeView{}, ObjectInfo{}, SpecSummary{}, ListenerSpec{}, AllowedRoutes{}, RuleSpec{},
		MatchSpec{}, ValueMatch{}, KeyMatch{}, FilterSpec{}, BackendRefSpec{}, DelegationSpec{},
		ConditionBlock{}, ConditionRow{}, Relations{}, ParentSummary{}, ListenerRoutes{},
		RouteLink{}, Condition{}, DelegationChild{}, Backend{}, DeclaredRefs{},
		VersionView{}, BuildInfo{}, InstallVersion{}, ProxyVersion{}, CRDVersion{},
		load.Input{}, objects.Ref{}, objects.ParentRef{}, objects.GroupKind{},
	}
	doc := docFields(t, "../../docs/output.md")
	for _, v := range types {
		rt := reflect.TypeOf(v)
		name := rt.Name()
		if rt == reflect.TypeOf(load.Input{}) {
			name = "Input"
		}
		got, ok := doc[name]
		if !ok {
			t.Errorf("%s: no table in docs/output.md", name)
			continue
		}
		want := jsonFields(rt)
		for _, f := range want {
			if !slices.Contains(got, f) {
				t.Errorf("%s: field %q is in the code but not in docs/output.md", name, f)
			}
		}
		for _, f := range got {
			if !slices.Contains(want, f) {
				t.Errorf("%s: field %q is in docs/output.md but not in the code", name, f)
			}
		}
	}
}
