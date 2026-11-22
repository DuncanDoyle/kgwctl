package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

func TestManifestEnvelope(t *testing.T) {
	v := "5m"
	m := Manifest{
		Build:        BuildInfo{Version: "v1"},
		Flags:        []FlagValue{{Name: "--timeout", Value: &v}, {Name: "-n"}},
		Start:        time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Finalization: "complete",
		Items: []Item{{
			Path: "a.yaml", Source: pb.Source{Operation: pb.OpList, Identity: pb.IdentityVerified}, Status: pb.ItemOK,
		}},
	}
	var buf bytes.Buffer
	if err := WriteManifest(&buf, m); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env["schemaVersion"] == nil || env["kind"] != "BundleManifest" || env["item"] == nil {
		t.Fatalf("bad envelope: %s", buf.String())
	}
	item := env["item"].(map[string]any)
	if item["start"] != "2026-11-22T10:00:00Z" {
		t.Errorf("start = %v", item["start"])
	}
	for _, k := range []string{"sealed", "end", "context", "server", "replicas", "collectors", "excluded", "truncated", "itemsOmitted"} {
		if _, ok := item[k]; ok {
			t.Errorf("empty field %q not omitted", k)
		}
	}
	first := item["items"].([]any)[0].(map[string]any)
	if _, ok := first["capturedAt"]; ok {
		t.Error("zero capturedAt not omitted")
	}
	if first["source"].(map[string]any)["operation"] != "list" {
		t.Errorf("source = %v", first["source"])
	}
	flags := item["flags"].([]any)
	if _, ok := flags[1].(map[string]any)["value"]; ok {
		t.Error("name-only flag has a value")
	}
}

func bigManifest() Manifest {
	m := Manifest{Build: BuildInfo{Version: "v1", Commit: strings.Repeat("c", 1000)}, Finalization: "complete"}
	for i := range 100000 {
		m.Items = append(m.Items, Item{Path: fmt.Sprintf("ns/%d/obj.yaml", i), Size: int64(i), Status: pb.ItemOK})
	}
	for i := range 20000 {
		m.Replicas = append(m.Replicas, pb.Replica{Namespace: "ns", Pod: fmt.Sprintf("pod-%d", i), Role: pb.RoleProxy})
	}
	var unwatched, selectors []string
	for i := range 50000 {
		unwatched = append(unwatched, fmt.Sprintf("ns-%d", i))
	}
	for i := range 100 {
		selectors = append(selectors, fmt.Sprintf("k%d=%s", i, strings.Repeat("v", 300)))
	}
	m.Scope = pb.Scope{Namespace: "ns", Installs: []pb.Install{
		{Namespace: "kgateway-system", Deployment: "kgateway", Discovery: pb.DiscoveryScope{Mode: "selectors", Unwatched: unwatched}},
		{Namespace: "other", Deployment: "kgw", Discovery: pb.DiscoveryScope{Mode: "selectors", Selectors: selectors}},
	}}
	m.Excluded = []Exclusion{{Kind: "gateway.networking.k8s.io/HTTPRoute", Reason: pb.ExcludeScope, Count: 3}}
	return m
}

func encodedSize(t *testing.T, m Manifest) int {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteManifest(&buf, m); err != nil {
		t.Fatal(err)
	}
	return buf.Len()
}

func TestManifestBound(t *testing.T) {
	m := bigManifest()
	const limit = 1 << 20
	Bound(&m, limit)
	if n := encodedSize(t, m); n > limit {
		t.Fatalf("encoded %d > %d", n, limit)
	}
	if m.ItemsOmitted <= 0 || m.ReplicasOmitted <= 0 || m.Truncated {
		t.Errorf("omitted items=%d replicas=%d truncated=%v", m.ItemsOmitted, m.ReplicasOmitted, m.Truncated)
	}
	if m.ItemsOmitted+len(m.Items) != 100000 {
		t.Errorf("items %d + omitted %d != 100000", len(m.Items), m.ItemsOmitted)
	}
	d := m.Scope.Installs[0].Discovery
	if d.UnwatchedOmitted <= 0 || d.UnwatchedOmitted+len(d.Unwatched) != 50000 {
		t.Errorf("unwatched %d omitted %d", len(d.Unwatched), d.UnwatchedOmitted)
	}
	if len(m.Excluded) != 1 || m.Excluded[0].Count != 3 {
		t.Errorf("excluded cut: %v", m.Excluded)
	}
}

func TestManifestBoundSelectors(t *testing.T) {
	m := bigManifest()
	Bound(&m, 0)
	d := m.Scope.Installs[1].Discovery
	if len(d.Selectors) != 64 || d.SelectorsOmitted != 36 {
		t.Errorf("selectors %d omitted %d", len(d.Selectors), d.SelectorsOmitted)
	}
	for _, s := range d.Selectors {
		if len(s) > 256 {
			t.Fatalf("selector %d bytes", len(s))
		}
	}
	if len(m.Build.Commit) != 256 {
		t.Errorf("commit %d bytes", len(m.Build.Commit))
	}
}

func TestManifestBoundExcludedOverflow(t *testing.T) {
	var m Manifest
	for i := range 3000 {
		reason := pb.ExcludeScope
		if i%2 == 0 {
			reason = pb.ExcludeOwnership
		}
		m.Excluded = append(m.Excluded, Exclusion{Kind: fmt.Sprintf("g/K%d", i), Reason: reason, Count: 2})
	}
	Bound(&m, 0)
	if len(m.Excluded) > 1024 {
		t.Fatalf("%d entries", len(m.Excluded))
	}
	total := 0
	others := map[pb.ExcludeReason]int{}
	for _, e := range m.Excluded {
		total += e.Count
		if e.Kind == "other" {
			others[e.Reason]++
		}
	}
	if total != 6000 || others[pb.ExcludeScope] != 1 || others[pb.ExcludeOwnership] != 1 {
		t.Errorf("total %d others %v", total, others)
	}
}

func TestManifestBoundMinimal(t *testing.T) {
	m := bigManifest()
	for i := range 200 {
		m.Scope.Installs = append(m.Scope.Installs, pb.Install{Namespace: "n", Deployment: fmt.Sprintf("d%d", i)})
	}
	// An unbounded Context keeps the stripped manifest over the limit, which forces the fallback.
	m.Context = strings.Repeat("x", 100000)
	Bound(&m, 4096)
	if !m.Truncated {
		t.Fatal("expected minimal manifest")
	}
	if len(m.Items) != 0 || len(m.Replicas) != 0 || len(m.Collectors) != 0 || m.ItemsOmitted != 100000 || m.ReplicasOmitted != 20000 {
		t.Errorf("not minimal: %d %d %d", len(m.Items), len(m.Replicas), len(m.Collectors))
	}
	if len(m.Scope.Installs) != 64 || m.Scope.InstallsOmitted != 138 || m.Scope.Namespace != "ns" {
		t.Errorf("scope installs %d omitted %d", len(m.Scope.Installs), m.Scope.InstallsOmitted)
	}
	if len(m.Excluded) != 1 || m.Build.Version != "v1" || len(m.Build.Commit) > 256 {
		t.Errorf("minimal fields: %v %v", m.Excluded, m.Build)
	}
	if n := encodedSize(t, m); n > 64<<10 {
		t.Errorf("minimal manifest %d bytes", n)
	}
}
