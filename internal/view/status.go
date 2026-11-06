package view

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DuncanDoyle/kgateway-ui/pkg/explore"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// Cell values with no condition to show (design §3.1).
const (
	cellNoStatus = "<no status>"
	cellStale    = "<stale>"
)

// ObjectCondition renders obj's condType condition as True, "False (<Reason>)",
// "Unknown (<Reason>)", <no status> or <stale> (design §3.1).
func ObjectCondition(obj *unstructured.Unstructured, condType string) string {
	c := objects.FindCondition(objects.Conditions(obj), condType)
	switch {
	case c == nil:
		return cellNoStatus
	// Ruling R25: Unknown never observed is a pending default, not stale status.
	case objects.IsStale(obj, *c) && (c.Status != metav1.ConditionUnknown || c.ObservedGeneration != 0):
		return cellStale
	case c.Status == metav1.ConditionTrue, c.Reason == "":
		return string(c.Status)
	}
	return fmt.Sprintf("%s (%s)", c.Status, c.Reason)
}

// ParentState is the state of one route parent or policy ancestor (design §3.1).
type ParentState string

// Parent states.
const (
	StateAccepted  ParentState = "accepted"
	StateRejected  ParentState = "rejected"
	StateNoStatus  ParentState = "no status"
	StateStale     ParentState = "stale"
	StateOther     ParentState = "other"
	StateUnwatched ParentState = "unwatched"
	StateDeclared  ParentState = "declared"
)

// summaryOrder is the order of the counts in a summary cell.
var summaryOrder = []ParentState{StateRejected, StateStale, StateNoStatus, StateUnwatched, StateDeclared, StateOther}

// ParentSummary is the state of one route parent or policy ancestor for one condition type.
type ParentSummary struct {
	// Parent is the parentRef or ancestorRef, with defaults applied.
	Parent objects.ParentRef `json:"parent"`
	State  ParentState       `json:"state"`
	// Reason is the deciding condition's reason, when there is one.
	Reason string `json:"reason,omitempty"`
	// Controller is the deciding entry's controller; for other, every
	// controller that wrote an entry, sorted and comma-separated.
	Controller string `json:"controller,omitempty"`
}

// entry is one status entry for a parent: a route's status.parents or a policy's status.ancestors item.
type entry struct {
	controller string
	conds      []metav1.Condition
}

// RouteParents returns the state of each distinct spec parentRef of route for
// condType, in spec order (design §3.1).
func RouteParents(r *load.Result, f *Filter, route *unstructured.Unstructured, condType string) []ParentSummary {
	status := objects.RouteParents(route)
	var out []ParentSummary
	var seen []objects.ParentRef
	for _, p := range objects.RouteParentRefs(route) {
		if slices.Contains(seen, p) {
			continue
		}
		seen = append(seen, p)
		var es []entry
		for _, s := range status {
			if s.ParentRef == p {
				es = append(es, entry{s.ControllerName, s.Conditions})
			}
		}
		ps := f.decideParent(route, p, es, condType)
		// The index decides only parents with no entry from ours.
		if ps.State == StateNoStatus && ps.Controller == "" || ps.State == StateOther {
			if st, ok := f.indexState(r, objects.RefOf(route), p); ok {
				ps = ParentSummary{Parent: p, State: st}
			}
		}
		out = append(out, ps)
	}
	return out
}

// PolicyAncestors returns the state of each distinct ancestor in policy's
// status.ancestors for condType, in status order (design §3.1).
func PolicyAncestors(f *Filter, policy *unstructured.Unstructured, condType string) []ParentSummary {
	as := objects.PolicyAncestors(policy)
	var out []ParentSummary
	var seen []objects.ParentRef
	for _, a := range as {
		if slices.Contains(seen, a.AncestorRef) {
			continue
		}
		seen = append(seen, a.AncestorRef)
		var es []entry
		for _, b := range as {
			if b.AncestorRef == a.AncestorRef {
				es = append(es, entry{b.ControllerName, b.Conditions})
			}
		}
		out = append(out, f.decideParent(policy, a.AncestorRef, es, condType))
	}
	return out
}

// decideParent applies design §3.1 to the entries es for parent p of obj:
// the owning install's entry, else the worst of ours, else other.
func (f *Filter) decideParent(obj *unstructured.Unstructured, p objects.ParentRef, es []entry, condType string) ParentSummary {
	ours := slices.DeleteFunc(slices.Clone(es), func(e entry) bool { return !f.Ours(e.controller) })
	if len(ours) == 0 {
		if len(es) == 0 {
			return ParentSummary{Parent: p, State: StateNoStatus}
		}
		cs := make([]string, 0, len(es))
		for _, e := range es {
			cs = append(cs, e.controller)
		}
		slices.Sort(cs)
		return ParentSummary{Parent: p, State: StateOther, Controller: strings.Join(slices.Compact(cs), ", ")}
	}
	if i, ok := f.installOfRef(p.Ref()); ok {
		names := f.r.Model.Installs[i].Resolved.ControllerNames
		if owned := slices.DeleteFunc(slices.Clone(ours), func(e entry) bool { return !slices.Contains(names, e.controller) }); len(owned) > 0 {
			ours = owned
		}
	}
	var worst ParentSummary
	for i, e := range ours {
		if ps := entrySummary(obj, p, e, condType); i == 0 || rank(ps.State) < rank(worst.State) {
			worst = ps
		}
	}
	return worst
}

// entrySummary is the state one of our entries gives its parent.
func entrySummary(obj *unstructured.Unstructured, p objects.ParentRef, e entry, condType string) ParentSummary {
	ps := ParentSummary{Parent: p, State: StateNoStatus, Controller: e.controller}
	c := objects.FindCondition(e.conds, condType)
	if c == nil {
		return ps
	}
	ps.Reason = c.Reason
	switch {
	case objects.IsStale(obj, *c):
		ps.State = StateStale
	case c.Status == metav1.ConditionTrue:
		ps.State = StateAccepted
	case c.Status == metav1.ConditionFalse:
		ps.State = StateRejected
	}
	return ps
}

// rank orders the states our entries give, worst first.
func rank(s ParentState) int {
	return slices.Index([]ParentState{StateRejected, StateStale, StateNoStatus, StateAccepted}, s)
}

// indexState returns unwatched or declared when the explore index of the
// install owning parent p places route there. It is false while an index input is not ok.
func (f *Filter) indexState(r *load.Result, route objects.Ref, p objects.ParentRef) (ParentState, bool) {
	if f.indexDown != "" {
		return "", false
	}
	i, ok := f.installOfRef(p.Ref())
	if !ok || r.Model.Installs[i].Index == nil {
		return "", false
	}
	ix := r.Model.Installs[i].Index
	id, ok := ix.Row(route)
	if !ok {
		return "", false
	}
	for _, l := range ix.Routes[id].Parents {
		if l.Ref != p {
			continue
		}
		switch l.Attachment {
		case explore.AttachmentUnwatched:
			return StateUnwatched, true
		case explore.AttachmentDeclared:
			return StateDeclared, true
		}
	}
	return "", false
}

// IndexUnavailable names the index inputs that are not ok, as "httproutes: denied".
// While it is not empty, RouteParents never returns unwatched or declared (design §6.3).
func (f *Filter) IndexUnavailable() string { return f.indexDown }

// indexInputs are the inputs of the explore index (design §6.3).
func indexInputs(served []kinds.Served) []schema.GroupVersionResource {
	out := ownershipInputs(served)
	for _, k := range kinds.ByCategory(served, kinds.CategoryRoute) {
		out = append(out, k.GVR())
	}
	return out
}

// Summarize renders ps as one cell: True when all are accepted, else
// "<accepted>/<total> (<counts>)" in the design §3.1 order.
func Summarize(ps []ParentSummary) string {
	if len(ps) == 0 {
		return cellNoStatus
	}
	counts := map[ParentState]int{}
	var reason string
	for _, p := range ps {
		counts[p.State]++
		if p.State == StateRejected {
			reason = p.Reason
		}
	}
	if counts[StateAccepted] == len(ps) {
		return string(metav1.ConditionTrue)
	}
	var parts []string
	for _, s := range summaryOrder {
		n := counts[s]
		if n == 0 {
			continue
		}
		part := fmt.Sprintf("%d %s", n, s)
		if s == StateRejected && n == 1 && reason != "" {
			part += ": " + reason
		}
		parts = append(parts, part)
	}
	return fmt.Sprintf("%d/%d (%s)", counts[StateAccepted], len(ps), strings.Join(parts, ", "))
}
