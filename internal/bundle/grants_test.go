package bundle

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// TestWriterGrantLines: errors.txt ends with one line per grant and
// namespace, whichever collectors were denied; per-item lines stay. A denied
// list or get gives a reader line, cluster-wide when it names no namespace.
// Failures add no grant line.
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
	h.w.Sink("resources").Error(pb.Path{"resources", "core", "Service", "ns-a"}, pb.Source{Namespace: "ns-a", Operation: pb.OpList}, denied)
	h.w.Sink("workloads").Error(pb.Path{"workloads", "ns-a", "pods.yaml"}, pb.Source{Namespace: "ns-a", Operation: pb.OpList}, denied)
	h.w.Sink("resources").Error(pb.Path{"resources", "core", "GatewayClass"}, pb.Source{Operation: pb.OpList}, denied)
	h.w.Sink("version").Error(pb.Path{"cluster"}, pb.Source{}, denied) // discovery
	h.w.Sink("logs").Error(pb.Path{"proxies", "ns-c", "p", "envoy.log"}, pb.Source{Namespace: "ns-c", Pod: "p", Operation: pb.OpLog}, errors.New("reset"))
	h.w.Sink("workloads").Error(pb.Path{"workloads", "ns-c", "pods.yaml"}, pb.Source{Namespace: "ns-c", Operation: pb.OpList}, errors.New("reset"))
	a := h.finish(t, Manifest{}, "version", "resources", "workloads", "logs", "proxy-metrics", "proxy-admin")

	b, _ := a.File(pb.ErrorsFile)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	want := []string{
		"grant: create pods/portforward in namespace ns-a: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
		"grant: create pods/portforward in namespace ns-b: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
		"grant: get pods/log in namespace ns-a: see the kgwctl-bundle-logs Role in docs/rbac.yaml",
		"grant: get pods/proxy in namespace ns-a: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
		"grant: get, list and watch cluster-wide: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
		"grant: get, list and watch in namespace ns-a: see the kgwctl-reader ClusterRole in docs/rbac.yaml",
	}
	if len(lines) < len(want)+11 {
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

// TestWriterGrantLinesAfterCut: item lines past the errors cap are cut with a
// marker, and the grant lines still end the file, within the cap.
func TestWriterGrantLinesAfterCut(t *testing.T) {
	h := newHarness(t, mustLimits(t, MinSize), redact.Options{}, "")
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", errors.New("no"))
	cols := []string{"c0", "c1", "c2", "c3"}
	for _, c := range cols {
		for i := range 4000 {
			pod := fmt.Sprintf("pod-with-a-long-name-%05d", i)
			h.w.Sink(c).Error(pb.Path{"proxies", "ns-a", pod, "envoy.log"}, pb.Source{Namespace: "ns-a", Pod: pod, Operation: pb.OpLog}, denied)
		}
	}
	a := h.finish(t, Manifest{}, cols...)
	b, _ := a.File(pb.ErrorsFile)
	if int64(len(b)) > h.w.lim.Errors {
		t.Errorf("errors.txt is %d bytes, cap %d", len(b), h.w.lim.Errors)
	}
	const tail = "[kgwctl: truncated: size cap]\ngrant: get pods/log in namespace ns-a: see the kgwctl-bundle-logs Role in docs/rbac.yaml\n"
	if !strings.HasSuffix(string(b), tail) {
		t.Errorf("errors.txt ends with %q", b[max(0, len(b)-200):])
	}
}
