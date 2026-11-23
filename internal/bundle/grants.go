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
	reader      = grant{"get, list and watch", "the kgwctl-reader ClusterRole"}
)

// grants maps each operation to the grant a 403 on it names (design §6). A
// list, get, discovery (unset) or check denial needs the reader role.
var grants = map[pb.Op]grant{
	pb.OpLog: podLog, pb.OpPreviousLog: podLog, pb.OpMetrics: podProxy,
	pb.OpConfigDump: portForward, pb.OpConfigDumpEDS: portForward, pb.OpStats: portForward,
	pb.OpServerInfo: portForward, pb.OpCerts: portForward, pb.OpClusters: portForward,
	0: reader, pb.OpList: reader, pb.OpCheck: reader,
}

// noteGrant records the grant a denied item needs, before any record bound
// applies; the caller holds w.mu. A reader denial without a namespace is
// cluster-wide; a subresource always names one.
func (w *Writer) noteGrant(it Item) {
	g, ok := grants[it.Source.Operation]
	ns := it.Source.Namespace
	if it.Status != pb.ItemDenied || !ok || ns == invalidPart || (ns == "" && g != reader) {
		return
	}
	if w.granted == nil {
		w.granted = map[grantKey]bool{}
	}
	w.granted[grantKey{g, ns}] = true
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
		where := "in namespace " + k.ns
		if k.ns == "" {
			where = "cluster-wide"
		}
		out[i] = "grant: " + k.g.verb + " " + where + ": see " + k.g.role + " in docs/rbac.yaml"
	}
	return out
}
