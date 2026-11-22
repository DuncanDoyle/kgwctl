package bundle

import (
	"os"
	"reflect"
	"regexp"
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

	for _, s := range []pb.CollectorStatus{pb.CollectorOK, pb.CollectorPartial, pb.CollectorFailed, pb.CollectorSkipped, pb.CollectorTimeout} {
		if !strings.Contains(subs["CollectorEntry"], "`"+string(s)+"`") {
			t.Errorf("collector status %q not documented", s)
		}
	}
	for _, s := range []pb.ItemStatus{pb.ItemOK, pb.ItemSkipped, pb.ItemDenied, pb.ItemDisabled, pb.ItemFailed, pb.ItemTimeout, pb.ItemDropped} {
		if !strings.Contains(subs["ManifestItem"], "`"+string(s)+"`") {
			t.Errorf("item status %q not documented", s)
		}
	}
}
