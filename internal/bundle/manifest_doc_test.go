package bundle

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// docTypes maps each manifest Go type to its heading under "## BundleManifest".
var docTypes = map[string]reflect.Type{
	"BundleManifest":   reflect.TypeFor[Manifest](),
	"ManifestBuild":    reflect.TypeFor[BuildInfo](),
	"FlagValue":        reflect.TypeFor[FlagValue](),
	"CollectorEntry":   reflect.TypeFor[CollectorEntry](),
	"Exclusion":        reflect.TypeFor[Exclusion](),
	"ManifestItem":     reflect.TypeFor[Item](),
	"ItemSource":       reflect.TypeFor[pb.Source](),
	"Scope":            reflect.TypeFor[pb.Scope](),
	"Install":          reflect.TypeFor[pb.Install](),
	"DiscoveryScope":   reflect.TypeFor[pb.DiscoveryScope](),
	"Window":           reflect.TypeFor[pb.Window](),
	"Replica":          reflect.TypeFor[pb.Replica](),
	"ReplicaContainer": reflect.TypeFor[pb.Container](),
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

var fieldRow = regexp.MustCompile("(?m)^\\| `([A-Za-z]+)` \\|")

func TestBundleManifestDocMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../docs/output.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), "\n## BundleManifest\n")
	if !ok {
		t.Fatal("docs/output.md has no BundleManifest section")
	}
	section, _, _ := strings.Cut(rest, "\n## ")

	subs := map[string]string{}
	parts := strings.Split(section, "\n### ")
	for _, p := range parts[1:] {
		head, body, _ := strings.Cut(p, "\n")
		subs[strings.TrimSpace(head)] = body
	}
	for name, typ := range docTypes {
		body, ok := subs[name]
		if !ok {
			t.Errorf("no ### %s subsection", name)
			continue
		}
		var got []string
		for _, m := range fieldRow.FindAllStringSubmatch(body, -1) {
			got = append(got, m[1])
		}
		sort.Strings(got)
		if want := jsonFields(typ); !reflect.DeepEqual(got, want) {
			t.Errorf("### %s lists %v, JSON fields are %v", name, got, want)
		}
	}
	if len(subs) != len(docTypes) {
		t.Errorf("section has %d subsections, want %d", len(subs), len(docTypes))
	}

	enums := []struct {
		sub, field string
		want       []string
	}{
		{"CollectorEntry", "status", []string{"ok", "partial", "failed", "skipped", "timeout"}},
		{"ManifestItem", "status", []string{"ok", "skipped", "denied", "disabled", "failed", "timeout", "dropped"}},
		{"Exclusion", "reason", reasonValues()},
		{"ItemSource", "identity", identityValues()},
	}
	for _, e := range enums {
		row := regexp.MustCompile("(?m)^\\| `" + e.field + "` \\|.*$").FindString(subs[e.sub])
		var got []string
		for _, m := range regexp.MustCompile("`([a-z-]+)`").FindAllStringSubmatch(row, -1)[1:] {
			got = append(got, m[1])
		}
		sort.Strings(got)
		want := slices.Sorted(slices.Values(e.want))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("### %s %s documents %v, want %v", e.sub, e.field, got, want)
		}
	}
}

// reasonValues and identityValues are the values the Sink accepts.
func reasonValues() []string {
	var out []string
	for r := range excludeReasons {
		out = append(out, string(r))
	}
	return out
}

func identityValues() []string {
	var out []string
	for i := range identities {
		if i != "" {
			out = append(out, string(i))
		}
	}
	return out
}
