package view

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/explore"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
)

// DescribeView is one object in full: the output of describe (design §3.2).
type DescribeView struct {
	Object ObjectInfo `json:"object"`
	// Spec is nil for kinds without a typed view.
	Spec       *SpecSummary     `json:"spec,omitempty"`
	Conditions []ConditionBlock `json:"conditions"`
	// Relations is empty for kinds that have none.
	Relations Relations `json:"relations,omitzero"`
	// Incomplete are the inputs the relations depend on that are not ok (design §6.3).
	Incomplete []load.Input `json:"incomplete,omitempty"`
}

// ObjectInfo identifies the object and its generations.
type ObjectInfo struct {
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Install is the owning install; "" when none owns the object or the kind is not attributed.
	Install string `json:"install,omitempty"`
	// InstallUnknown names the ownership inputs that are not ok.
	InstallUnknown    string `json:"installUnknown,omitempty"`
	CreationTimestamp string `json:"creationTimestamp"`
	Age               string `json:"age"`
	Generation        int64  `json:"generation"`
	// ObservedGeneration is status.observedGeneration, else the newest of the object's conditions; 0 when none.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// SpecSummary is the spec of a Gateway, ListenerSet or HTTPRoute.
type SpecSummary struct {
	Class     string         `json:"class,omitempty"`
	Addresses []string       `json:"addresses,omitempty"`
	Parent    string         `json:"parent,omitempty"`
	Hostnames []string       `json:"hostnames,omitempty"`
	Listeners []ListenerSpec `json:"listeners,omitempty"`
	Rules     []RuleSpec     `json:"rules,omitempty"`
}

// ListenerSpec is one spec listener.
type ListenerSpec struct {
	Name     string `json:"name"`
	Port     int32  `json:"port,omitempty"`
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname,omitempty"`
	// TLS is the TLS mode; "" without a TLS block.
	TLS           string `json:"tls,omitempty"`
	AllowedRoutes string `json:"allowedRoutes"`
}

// RuleSpec is one HTTPRoute rule.
type RuleSpec struct {
	Name        string   `json:"name,omitempty"`
	Matches     []string `json:"matches,omitempty"`
	Filters     []string `json:"filters,omitempty"`
	BackendRefs []string `json:"backendRefs,omitempty"`
}

// ConditionBlock is one status block and the controller that wrote it.
type ConditionBlock struct {
	// Scope is "object", "listener <name>", "parent <ref>" or "ancestor <ref>".
	Scope      string         `json:"scope"`
	Controller string         `json:"controller,omitempty"`
	Conditions []ConditionRow `json:"conditions"`
}

// ConditionRow is one condition.
type ConditionRow struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransition     string `json:"lastTransition,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration"`
	// Stale follows ObjectCondition: behind metadata.generation, unless Unknown and never observed.
	Stale bool `json:"stale,omitempty"`
}

// Relations are what the object relates to (design §3.2).
type Relations struct {
	// Resolved is set when the owning install's explore index has the object.
	Resolved bool `json:"resolved"`
	// Unknown names the index inputs that are not ok; the relations are then not resolved.
	Unknown string `json:"unknown,omitempty"`
	// Parents are a route's parents with their states, as get counts them.
	Parents []ParentSummary `json:"parents,omitempty"`
	// DuplicateParents are spec parentRefs written more than once.
	DuplicateParents []string `json:"duplicateParents,omitempty"`
	// AttachedRoutes and NotAccepted are a Gateway's or ListenerSet's routes.
	AttachedRoutes []ListenerRoutes `json:"attachedRoutes,omitempty"`
	NotAccepted    []RouteRef       `json:"notAccepted,omitempty"`
	ListenerSets   []string         `json:"listenerSets,omitempty"`
	// DelegationParents and DelegationChildren are a route's delegation edges.
	DelegationParents  []string `json:"delegationParents,omitempty"`
	DelegationChildren []string `json:"delegationChildren,omitempty"`
	Backends           []string `json:"backends,omitempty"`
	// Ancestors are a policy's status.ancestors; targets are not resolved (E7).
	Ancestors []ParentSummary `json:"ancestors,omitempty"`
	// Declared is set when the relations are not resolved: what the spec names.
	Declared *DeclaredRefs `json:"declared,omitempty"`
	// Of says which relations apply: RelationsGateway, RelationsRoute or RelationsPolicy.
	Of string `json:"-"`
}

// Relation shapes, by the described object's kind.
const (
	RelationsGateway = "gateway"
	RelationsRoute   = "route"
	RelationsPolicy  = "policy"
)

// ListenerRoutes are the routes attached to one listener.
type ListenerRoutes struct {
	// Listener is the listener name, "<ListenerSet>:<name>" through a ListenerSet, or "*" when not known.
	Listener string   `json:"listener"`
	Routes   []string `json:"routes"`
}

// RouteRef is a route that names the object but is not accepted on it.
type RouteRef struct {
	Route      string `json:"route"`
	Listener   string `json:"listener,omitempty"`
	Attachment string `json:"attachment"`
	// Condition is the deciding condition, or why the status is pending.
	Condition string `json:"condition,omitempty"`
}

// DeclaredRefs are the references an object's spec makes, not resolved.
type DeclaredRefs struct {
	ParentRefs  []string `json:"parentRefs,omitempty"`
	BackendRefs []string `json:"backendRefs,omitempty"`
}

// listenerUnknown is the listener of an accepted link the index does not place.
const listenerUnknown = "*"

// Describe returns the view of object name of target t in namespace ns ("" for any), AGE at now.
// It shows the object even when get would hide it (Ruling R29).
func Describe(r *load.Result, f *Filter, t resolve.Target, ns, name string, now time.Time) (*DescribeView, error) {
	if err := f.pinErr(); err != nil {
		return nil, err
	}
	var found []row
	for _, k := range t.Kinds {
		in, ok := r.Inputs[k.GVR()]
		if !ok || in.State != load.StateOK {
			return nil, unreadable(in, k)
		}
		var hits []row
		for _, u := range r.Model.Snapshot.Objects[k.GVR()] {
			if u.GetName() == name && (ns == "" || u.GetNamespace() == "" || u.GetNamespace() == ns) {
				hits = append(hits, row{k, u})
			}
		}
		if len(hits) > 1 {
			var nss []string
			for _, h := range hits {
				nss = append(nss, h.u.GetNamespace())
			}
			slices.Sort(nss)
			return nil, fmt.Errorf("ambiguous: %s %q exists in namespaces %s; use -n", k.Resource, name, strings.Join(nss, ", "))
		}
		found = append(found, hits...)
	}
	switch len(found) {
	case 0:
		return nil, &NotFoundError{Resource: resourceOf(t), Name: name}
	case 1:
	default:
		var ks []string
		for _, h := range found {
			ks = append(ks, h.k.Kind)
		}
		return nil, fmt.Errorf("ambiguous: name exists as %s; use listenersets.<group>", strings.Join(ks, " and "))
	}
	k, u := found[0].k, found[0].u
	v := &DescribeView{Object: objectInfo(r, f, k, u, now), Spec: specSummary(k, u), Conditions: conditionBlocks(k, u)}
	v.Relations, v.Incomplete = relations(r, f, k, u)
	return v, nil
}

func objectInfo(r *load.Result, f *Filter, k kinds.Served, u *unstructured.Unstructured, now time.Time) ObjectInfo {
	o := ObjectInfo{Kind: k.Kind, Group: k.Group, Namespace: u.GetNamespace(), Name: u.GetName(),
		CreationTimestamp: u.GetCreationTimestamp().UTC().Format(time.RFC3339),
		Age:               duration.HumanDuration(now.Sub(u.GetCreationTimestamp().Time)), Generation: u.GetGeneration()}
	if attributed(k.Category) {
		if !f.known {
			o.InstallUnknown = unavailable(r, ownershipInputs(r.Served))
		} else if in, ok := f.InstallOf(u); ok {
			o.Install = in.Display()
		}
	}
	if g, ok, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration"); ok {
		o.ObservedGeneration = g
	} else {
		for _, c := range objects.Conditions(u) {
			o.ObservedGeneration = max(o.ObservedGeneration, c.ObservedGeneration)
		}
	}
	return o
}

// specSummary decodes the typed views of design §3.2; other kinds have none.
func specSummary(k kinds.Served, u *unstructured.Unstructured) *SpecSummary {
	switch {
	case k.Category == kinds.CategoryGateway:
		gv := objects.DecodeGateway(u)
		return &SpecSummary{Class: gv.Class, Addresses: gv.SpecAddresses, Listeners: listenerSpecs(gv.Listeners)}
	case k.Category == kinds.CategoryListenerSet:
		lv := objects.DecodeListenerSet(u)
		return &SpecSummary{Parent: refName(lv.Parent.Ref(), u.GetNamespace(), ""), Listeners: listenerSpecs(lv.Listeners)}
	case k.Group == objects.GatewayAPIGroup && k.Kind == "HTTPRoute":
		hv := objects.DecodeHTTPRoute(u)
		s := &SpecSummary{Hostnames: hv.Hostnames}
		for _, rl := range hv.Rules {
			s.Rules = append(s.Rules, ruleSpec(rl, u.GetNamespace()))
		}
		return s
	}
	return nil
}

func listenerSpecs(ls []objects.Listener) []ListenerSpec {
	out := make([]ListenerSpec, 0, len(ls))
	for _, l := range ls {
		s := ListenerSpec{Name: l.Name, Port: l.Port, Protocol: l.Protocol, Hostname: l.Hostname,
			AllowedRoutes: "from " + l.AllowedRoutes.Namespaces.From}
		if sel := l.AllowedRoutes.Namespaces.Selector; sel != nil {
			s.AllowedRoutes += " " + metav1.FormatLabelSelector(sel)
		}
		var gks []string
		for _, gk := range l.AllowedRoutes.Kinds {
			gks = append(gks, gk.Kind)
		}
		if len(gks) > 0 {
			s.AllowedRoutes += ", kinds " + strings.Join(gks, ",")
		}
		if l.TLS != nil {
			s.TLS = l.TLS.Mode
		}
		out = append(out, s)
	}
	return out
}

func ruleSpec(rl objects.Rule, ns string) RuleSpec {
	s := RuleSpec{Name: rl.Name}
	for _, m := range rl.Matches {
		var parts []string
		if m.Path != nil {
			parts = append(parts, m.Path.Type+" "+m.Path.Value)
		}
		for _, h := range m.Headers {
			parts = append(parts, fmt.Sprintf("header %s %s %s", h.Name, h.Type, h.Value))
		}
		for _, q := range m.QueryParams {
			parts = append(parts, fmt.Sprintf("query %s %s %s", q.Name, q.Type, q.Value))
		}
		if m.Method != "" {
			parts = append(parts, "method "+m.Method)
		}
		s.Matches = append(s.Matches, strings.Join(parts, ", "))
	}
	for _, fl := range rl.Filters {
		s.Filters = append(s.Filters, filterName(fl, ns))
	}
	for _, b := range rl.BackendRefs {
		s.BackendRefs = append(s.BackendRefs, backendRef(b, ns))
	}
	return s
}

func filterName(fl objects.Filter, ns string) string {
	switch {
	case fl.Mirror != nil:
		return fl.Type + " " + refName(fl.Mirror.Backend, ns, "Service")
	case fl.ExtensionRef != nil:
		return fl.Type + " " + refName(*fl.ExtensionRef, ns, "")
	}
	return fl.Type
}

func backendRef(b objects.BackendRef, ns string) string {
	if d := b.Delegation; d != nil {
		target := d.Name
		if d.Mode == objects.DelegationLabel {
			target = "label " + d.Label
		}
		if d.Namespace != ns {
			target = d.Namespace + "/" + target
		}
		return fmt.Sprintf("delegate (%s) %s", d.Mode, target)
	}
	s := refName(b.Ref, ns, "")
	if b.Port != 0 {
		s += ":" + strconv.Itoa(int(b.Port))
	}
	if !b.WeightDefaulted {
		s += fmt.Sprintf(" weight %d", b.Weight)
	}
	return s
}

// conditionBlocks returns the object's conditions, then one block per listener, parent or ancestor entry.
func conditionBlocks(k kinds.Served, u *unstructured.Unstructured) []ConditionBlock {
	ns := u.GetNamespace()
	out := []ConditionBlock{}
	if cs := objects.Conditions(u); len(cs) > 0 || k.Category != kinds.CategoryRoute && k.Category != kinds.CategoryPolicy {
		out = append(out, ConditionBlock{Scope: "object", Conditions: conditionRows(u, cs)})
	}
	switch k.Category {
	case kinds.CategoryGateway, kinds.CategoryListenerSet:
		for _, l := range objects.GatewayListeners(u) {
			out = append(out, ConditionBlock{Scope: "listener " + l.Name, Conditions: conditionRows(u, l.Conditions)})
		}
	case kinds.CategoryRoute:
		for _, p := range objects.RouteParents(u) {
			out = append(out, ConditionBlock{Scope: "parent " + ParentName(p.ParentRef, ns), Controller: p.ControllerName, Conditions: conditionRows(u, p.Conditions)})
		}
	case kinds.CategoryPolicy:
		for _, a := range objects.PolicyAncestors(u) {
			out = append(out, ConditionBlock{Scope: "ancestor " + ParentName(a.AncestorRef, ns), Controller: a.ControllerName, Conditions: conditionRows(u, a.Conditions)})
		}
	}
	return out
}

func conditionRows(u *unstructured.Unstructured, cs []metav1.Condition) []ConditionRow {
	out := make([]ConditionRow, 0, len(cs))
	for _, c := range cs {
		row := ConditionRow{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, ObservedGeneration: c.ObservedGeneration,
			Stale: objects.IsStale(u, c) && (c.Status != metav1.ConditionUnknown || c.ObservedGeneration != 0)}
		if !c.LastTransitionTime.IsZero() {
			row.LastTransition = c.LastTransitionTime.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

// ParentName is a parent or ancestor ref as "Kind/[namespace/]name[:section][:port]", seen from ns.
func ParentName(p objects.ParentRef, ns string) string {
	s := refName(p.Ref(), ns, "")
	if p.SectionName != "" {
		s += ":" + p.SectionName
	}
	if p.Port != 0 {
		s += ":" + strconv.Itoa(int(p.Port))
	}
	return s
}

// relations resolves u's relations from the owning install's index, or falls back to what u declares.
func relations(r *load.Result, f *Filter, k kinds.Served, u *unstructured.Unstructured) (Relations, []load.Input) {
	var rel Relations
	switch k.Category {
	case kinds.CategoryPolicy:
		return Relations{Of: RelationsPolicy, Ancestors: PolicyAncestors(f, u, "Accepted")}, nil
	case kinds.CategoryGateway, kinds.CategoryListenerSet:
		rel.Of = RelationsGateway
	case kinds.CategoryRoute:
		rel.Of = RelationsRoute
	default:
		return rel, nil
	}
	if f.indexDown != "" {
		rel.Unknown = f.indexDown
		rel.Declared = declared(k, u)
		var in []load.Input
		for _, gvr := range indexInputs(r.Served) {
			if x, ok := r.Inputs[gvr]; ok && x.State != load.StateOK && !slices.ContainsFunc(in, func(y load.Input) bool { return y.Resource == x.Resource }) {
				in = append(in, x)
			}
		}
		slices.SortFunc(in, func(a, b load.Input) int { return cmp.Compare(a.Resource, b.Resource) })
		return rel, in
	}
	ref := objects.RefOf(u)
	var ix *explore.Index
	if i, ok := f.installOfRef(ref); ok {
		ix = r.Model.Installs[i].Index
	}
	ns := u.GetNamespace()
	switch {
	case ix == nil:
	case k.Category == kinds.CategoryGateway:
		if id, ok := ix.FindGateway(ref); ok {
			gw := ix.Gateways[id]
			rel.Resolved = true
			rel.AttachedRoutes, rel.NotAccepted = listenerRoutes(ix, gw.Routes, ref, gw.View.Listeners, ns)
			for _, l := range gw.ListenerSets {
				rel.ListenerSets = append(rel.ListenerSets, refName(ix.ListenerSets[l].Ref, ns, ""))
			}
		}
	case k.Category == kinds.CategoryListenerSet:
		if id, ok := ix.FindListenerSet(ref); ok {
			ls := ix.ListenerSets[id]
			rel.Resolved = true
			rel.AttachedRoutes, rel.NotAccepted = listenerRoutes(ix, ls.Routes, ref, ls.View.Listeners, ns)
		}
	default:
		if id, ok := ix.Row(ref); ok {
			rr := ix.Routes[id]
			rel.Resolved = true
			rel.Parents = RouteParents(r, f, u, "Accepted")
			rel.DuplicateParents = duplicateParents(u)
			for _, p := range rr.ParentRoutes {
				rel.DelegationParents = append(rel.DelegationParents, refName(p, ns, ""))
			}
			for _, c := range ix.ByParentRoute[ref] {
				rel.DelegationChildren = append(rel.DelegationChildren, refName(ix.Routes[c].Ref, ns, ""))
			}
			for _, c := range ix.Declared[ref] {
				rel.DelegationChildren = append(rel.DelegationChildren, refName(ix.Routes[c].Ref, ns, "")+" (declared)")
			}
			rel.Backends = backendNames(k, u)
		}
	}
	if !rel.Resolved {
		rel.Declared = declared(k, u)
	}
	return rel, nil
}

// listenerRoutes groups the links of rows ids to target by listener: accepted ones as attached, the rest as not accepted.
func listenerRoutes(ix *explore.Index, ids []explore.RowID, target objects.Ref, spec []objects.Listener, ns string) ([]ListenerRoutes, []RouteRef) {
	attached := map[string][]string{}
	var notAccepted []RouteRef
	for _, id := range ids {
		rr := ix.Routes[id]
		name := refName(rr.Ref, ns, "")
		for _, l := range rr.Parents {
			if l.Target != target && (l.Via == nil || *l.Via != target) {
				continue
			}
			ls := linkListeners(ix, id, l, target, ns)
			if l.Attachment == explore.AttachmentAccepted {
				for _, li := range ls {
					if !slices.Contains(attached[li], name) {
						attached[li] = append(attached[li], name)
					}
				}
				continue
			}
			rf := RouteRef{Route: name, Listener: strings.Join(ls, ","), Attachment: string(l.Attachment)}
			switch {
			case l.Cond != nil:
				rf.Condition = fmt.Sprintf("%s=%s (%s)", l.Cond.Type, l.Cond.Status, l.Cond.Reason)
				if l.Cond.Message != "" {
					rf.Condition += ": " + l.Cond.Message
				}
			case l.Pending != "":
				rf.Condition = l.Pending
			}
			notAccepted = append(notAccepted, rf)
		}
	}
	// Spec listeners first, in spec order, then ListenerSet listeners and unknown ones by name.
	var order []string
	for _, l := range spec {
		if _, ok := attached[l.Name]; ok {
			order = append(order, l.Name)
		}
	}
	var rest []string
	for li := range attached {
		if !slices.Contains(order, li) {
			rest = append(rest, li)
		}
	}
	slices.Sort(rest)
	var out []ListenerRoutes
	for _, li := range append(order, rest...) {
		out = append(out, ListenerRoutes{Listener: li, Routes: attached[li]})
	}
	return out, notAccepted
}

// linkListeners names the listeners of target that link l lands on: its sectionName, else the index's computed hits.
func linkListeners(ix *explore.Index, id explore.RowID, l explore.ParentLink, target objects.Ref, ns string) []string {
	prefix := ""
	if l.Target != target {
		prefix = refName(l.Target, ns, "") + ":"
	}
	if l.Ref.SectionName != "" {
		return []string{prefix + l.Ref.SectionName}
	}
	var out []string
	if fs := ix.Facts(id, l); fs != nil {
		for _, h := range fs.Listeners {
			out = append(out, prefix+h.Listener)
		}
	}
	if len(out) == 0 {
		return []string{prefix + listenerUnknown}
	}
	return out
}

// duplicateParents lists the spec parentRefs written more than once after defaults (design §3.1).
func duplicateParents(u *unstructured.Unstructured) []string {
	ps := objects.RouteParentRefs(u)
	var out []string
	for i, p := range ps {
		if slices.Contains(ps[:i], p) {
			if s := ParentName(p, u.GetNamespace()); !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// declared is what u's spec names: parentRefs and backendRefs, unresolved.
func declared(k kinds.Served, u *unstructured.Unstructured) *DeclaredRefs {
	d := &DeclaredRefs{}
	ns := u.GetNamespace()
	switch k.Category {
	case kinds.CategoryListenerSet:
		d.ParentRefs = []string{ParentName(objects.ListenerSetParent(u), ns)}
	case kinds.CategoryRoute:
		for _, p := range objects.RouteParentRefs(u) {
			d.ParentRefs = append(d.ParentRefs, ParentName(p, ns))
		}
		d.BackendRefs = backendNames(k, u)
	}
	return d
}

// backendNames lists route u's distinct backends, mirrors marked.
func backendNames(k kinds.Served, u *unstructured.Unstructured) []string {
	var out []string
	for _, t := range backendTargets(k, u) {
		s := refName(t.Ref, u.GetNamespace(), "")
		if t.Mirror {
			s += " (mirror)"
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
