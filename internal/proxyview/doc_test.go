package proxyview_test

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
)

// jsonFields collects the JSON field names of t and every struct it holds.
func jsonFields(t reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice:
		jsonFields(t.Elem(), out, seen)
		return
	case reflect.Struct:
	default:
		return
	}
	if seen[t] || t.PkgPath() == "time" {
		return
	}
	seen[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			jsonFields(f.Type, out, seen)
			continue
		}
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			out[name] = true
		}
		jsonFields(f.Type, out, seen)
	}
}

// fieldRow matches a table row's first cell, such as "| `name` |".
var fieldRow = regexp.MustCompile("(?m)^\\| `([A-Za-z]+)` \\|")

// TestProxyRoutesDocMatchesPayload: every payload JSON field is in the
// ProxyRoutes section of docs/output.md, and the section names no other.
func TestProxyRoutesDocMatchesPayload(t *testing.T) {
	b, err := os.ReadFile("../../docs/output.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "\n## ProxyRoutes\n")
	if !ok {
		t.Fatal("docs/output.md has no ProxyRoutes section")
	}
	doc, _, _ := strings.Cut(rest, "\n## ")
	documented := map[string]bool{}
	for _, m := range fieldRow.FindAllStringSubmatch(doc, -1) {
		documented[m[1]] = true
	}
	payload := map[string]bool{}
	jsonFields(reflect.TypeOf(proxyview.ProxyRoutes{}), payload, map[reflect.Type]bool{})
	for f := range payload {
		if !documented[f] {
			t.Errorf("payload field %q is not documented", f)
		}
	}
	for f := range documented {
		if !payload[f] {
			t.Errorf("docs name field %q, which the payload lacks", f)
		}
	}
}
