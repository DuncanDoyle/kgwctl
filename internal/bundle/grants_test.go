package bundle

import (
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// TestWriterGrantLines: errors.txt ends with one line per grant and
// namespace, whichever collectors were denied; per-item lines stay. Denials
// without a known grant, and failures, add no grant line.
func TestWriterGrantLines(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", errors.New("no"))
	deny := func(collector, ns, file string, op pb.Op) {
		h.w.Sink(collector).Error(pb.Path{"proxies", ns, "p", file}, pb.Source{Namespace: ns, Pod: "p", Operation: op}, denied)
	}
	deny("version", "ns-a", "server_info.json", pb.OpServerInfo)
	deny("proxy-admin", "ns-a", "stats.txt", pb.OpStats)
	deny("proxy-admin", "ns-b", "stats.txt", pb.OpStats)
	deny("logs", "ns-a", "envoy.log", pb.OpLog)
	deny("logs", "ns-a", "envoy.previous.log", pb.OpPreviousLog)
	deny("proxy-metrics", "ns-a", "metrics.txt", pb.OpMetrics)
	deny("resources", "ns-a", "list.yaml", pb.OpList)
	h.w.Sink("logs").Error(pb.Path{"proxies", "ns-c", "p", "envoy.log"}, pb.Source{Namespace: "ns-c", Pod: "p", Operation: pb.OpLog}, errors.New("reset"))
	a := h.finish(t, Manifest{}, "version", "resources", "logs", "proxy-metrics", "proxy-admin")

	b, _ := a.File(pb.ErrorsFile)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	want := []string{
		"grant: create pods/portforward in namespace ns-a: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
		"grant: create pods/portforward in namespace ns-b: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
		"grant: get pods/log in namespace ns-a: see the kgwctl-bundle-logs Role in docs/rbac.yaml",
		"grant: get pods/proxy in namespace ns-a: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
	}
	if len(lines) < len(want)+8 {
		t.Fatalf("errors.txt:\n%s", b)
	}
	if got := lines[len(lines)-len(want):]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("errors.txt ends with\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Every item line comes before the grant lines.
	for _, l := range lines[:len(lines)-len(want)] {
		if strings.HasPrefix(l, "grant: ") {
			t.Errorf("grant line before an item line:\n%s", b)
		}
	}
}
