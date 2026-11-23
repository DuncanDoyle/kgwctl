package bundle

import (
	"cmp"
	"slices"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// grant is an RBAC grant a denied operation needs, and where docs/rbac.yaml has it.
type grant struct {
	verb, role string
}

// grantKey is one grant line: a grant in a namespace.
type grantKey struct {
	g  grant
	ns string
}

var (
	portForward = grant{"create pods/portforward", "the kgwctl-proxy-portforward Role"}
	podLog      = grant{"get pods/log", "the kgwctl-bundle-logs Role"}
	podProxy    = grant{"get pods/proxy", "the kgwctl-reader ClusterRole"}
)

// grants maps the operations of Pod subresource reads to their grant (design §6).
var grants = map[pb.Op]grant{
	pb.OpLog: podLog, pb.OpPreviousLog: podLog, pb.OpMetrics: podProxy,
	pb.OpConfigDump: portForward, pb.OpConfigDumpEDS: portForward, pb.OpStats: portForward,
	pb.OpServerInfo: portForward, pb.OpCerts: portForward, pb.OpClusters: portForward,
}

// noteGrant records the grant a denied item needs, before any record bound
// applies; the caller holds w.mu.
func (w *Writer) noteGrant(it Item) {
	g, ok := grants[it.Source.Operation]
	if it.Status != pb.ItemDenied || !ok || it.Source.Namespace == "" || it.Source.Namespace == invalidPart {
		return
	}
	if w.granted == nil {
		w.granted = map[grantKey]bool{}
	}
	w.granted[grantKey{g, it.Source.Namespace}] = true
}

// grantLines returns one line per grant and namespace, sorted.
func (w *Writer) grantLines() []string {
	keys := make([]grantKey, 0, len(w.granted))
	for k := range w.granted {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b grantKey) int {
		return cmp.Or(cmp.Compare(a.g.verb, b.g.verb), cmp.Compare(a.ns, b.ns))
	})
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = "grant: " + k.g.verb + " in namespace " + k.ns + ": see " + k.g.role + " in docs/rbac.yaml"
	}
	return out
}
