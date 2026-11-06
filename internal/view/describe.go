package view

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/explore"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
)

// DescribeView is one object in full: the output of describe (design §3.2).
// Its JSON form is structured; refs are absolute (Ruling R32).
type DescribeView struct {
	Object ObjectInfo `json:"object"`
	// Spec is nil for kinds without a typed view.
	Spec       *SpecSummary     `json:"spec,omitempty"`
	Conditions []ConditionBlock `json:"conditions"`
	// Relations is omitted for kinds that have none.
	Relations Relations `json:"relations,omitzero"`
	// Incomplete are the inputs the install and relations depend on that are not ok (design §6.3).
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
	// Age is for text output only.
	Age        string `json:"-"`
	Generation int64  `json:"generation"`
	// ObservedGeneration is status.observedGeneration, else the newest of the object's conditions; 0 when none.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// SpecSummary is the spec of a Gateway, ListenerSet or HTTPRoute.
type SpecSummary struct {
	Class     string             `json:"class,omitempty"`
	Addresses []string           `json:"addresses,omitempty"`
	Parent    *objects.ParentRef `json:"parent,omitempty"`
	Hostnames []string           `json:"hostnames,omitempty"`
	Listeners []ListenerSpec     `json:"listeners,omitempty"`
	Rules     []RuleSpec         `json:"rules,omitempty"`
}

// ListenerSpec is one spec listener.
type ListenerSpec struct {
	Name     string `json:"name"`
	Port     int32  `json:"port,omitempty"`
	Protocol string `json:"protocol"`
	Hostname string `json:"hostname,omitempty"`
	// TLSMode is "" without a TLS block.
	TLSMode       string        `json:"tlsMode,omitempty"`
	AllowedRoutes AllowedRoutes `json:"allowedRoutes"`
}

// AllowedRoutes is a listener's allowedRoutes, with defaults applied.
type AllowedRoutes struct {
	From string `json:"from"`
	// Selector is the namespace label selector in kubectl's string form.
	Selector string              `json:"selector,omitempty"`
	Kinds    []objects.GroupKind `json:"kinds,omitempty"`
}

// RuleSpec is one HTTPRoute rule.
type RuleSpec struct {
	Name        string           `json:"name,omitempty"`
	Matches     []MatchSpec      `json:"matches,omitempty"`
	Filters     []FilterSpec     `json:"filters,omitempty"`
	BackendRefs []BackendRefSpec `json:"backendRefs,omitempty"`
}

// MatchSpec is one rule match.
type MatchSpec struct {
	Path        *ValueMatch `json:"path,omitempty"`
	Headers     []KeyMatch  `json:"headers,omitempty"`
	QueryParams []KeyMatch  `json:"queryParams,omitempty"`
	Method      string      `json:"method,omitempty"`
}

// ValueMatch is a path match.
type ValueMatch struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// KeyMatch is a header or query parameter match.
type KeyMatch struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// FilterSpec is one rule filter; Target is set for ExtensionRef and RequestMirror.
type FilterSpec struct {
	Type   string       `json:"type"`
	Target *objects.Ref `json:"target,omitempty"`
}

// BackendRefSpec is one rule backendRef: a backend, or a delegation to child routes.
// The embedded Ref is nil for a delegation, so its fields are absent from JSON.
type BackendRefSpec struct {
	*objects.Ref
	Port int32 `json:"port,omitempty"`
	// Weight is nil when the spec leaves it to the default.
	Weight     *int32          `json:"weight,omitempty"`
	Delegation *DelegationSpec `json:"delegation,omitempty"`
}

// DelegationSpec is a delegation backendRef: mode name, wildcard or label.
type DelegationSpec struct {
	Mode      string `json:"mode"`
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"`
	Label     string `json:"label,omitempty"`
}

// Condition block scopes.
const (
	ScopeObject   = "object"
	ScopeListener = "listener"
	ScopeParent   = "parent"
	ScopeAncestor = "ancestor"
)

// ConditionBlock is one status block and the controller that wrote it.
type ConditionBlock struct {
	// Scope is ScopeObject, ScopeListener, ScopeParent or ScopeAncestor.
	Scope string `json:"scope"`
	// Listener names a listener block; Ref is a parent or ancestor block's ref.
	Listener   string             `json:"listener,omitempty"`
	Ref        *objects.ParentRef `json:"ref,omitempty"`
	Controller string             `json:"controller,omitempty"`
	Conditions []ConditionRow     `json:"conditions"`
}

// ConditionRow is one condition.
type ConditionRow struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransition     string `json:"lastTransition,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration"`
	// Stale follows StaleCondition.
	Stale bool `json:"stale,omitempty"`
}

// Relations are what the object relates to (design §3.2).
type Relations struct {
	// Resolved is set for Gateways, ListenerSets and routes: whether the owning install's index has the object.
	Resolved *bool `json:"resolved,omitempty"`
	// Unknown names the index inputs that are not ok; index-derived relations are then absent.
	Unknown string `json:"unknown,omitempty"`
	// Parents are a route's parents with their states, as get counts them.
	Parents []ParentSummary `json:"parents,omitempty"`
	// DuplicateParents are spec parentRefs written more than once.
	DuplicateParents []objects.ParentRef `json:"duplicateParents,omitempty"`
	// AttachedRoutes, NotAccepted and Pending are a Gateway's or ListenerSet's routes.
	AttachedRoutes []ListenerRoutes `json:"attachedRoutes,omitempty"`
	// NotAccepted are the links the controller rejected: the set get counts as "not accepted" (Ruling R30).
	NotAccepted []RouteLink `json:"notAccepted,omitempty"`
	// Pending are the links without a current status from the install: pending, unwatched or declared.
	Pending      []RouteLink   `json:"pending,omitempty"`
	ListenerSets []objects.Ref `json:"listenerSets,omitempty"`
	// DelegationParents and DelegationChildren are a route's delegation edges.
	DelegationParents  []objects.Ref     `json:"delegationParents,omitempty"`
	DelegationChildren []DelegationChild `json:"delegationChildren,omitempty"`
	Backends           []Backend         `json:"backends,omitempty"`
	// Ancestors are a policy's status.ancestors; targets are not resolved (E7).
	Ancestors []ParentSummary `json:"ancestors,omitempty"`
	// Declared is what an unresolved route or ListenerSet's spec names.
	Declared *DeclaredRefs `json:"declared,omitempty"`
	// Of says which relations apply, for text output.
	Of string `json:"-"`
}

// IsZero reports whether r has nothing for JSON, so omitzero drops it.
func (r Relations) IsZero() bool {
	return r.Resolved == nil && r.Unknown == "" && len(r.Parents) == 0 && len(r.Ancestors) == 0 && r.Declared == nil
}

// Relation shapes, by the described object's kind.
const (
	RelationsGateway     = "gateway"
	RelationsListenerSet = "listenerset"
	RelationsRoute       = "route"
	RelationsPolicy      = "policy"
)

// ListenerRoutes are the routes attached to one listener of Parent.
type ListenerRoutes struct {
	// Parent is the Gateway or ListenerSet the listener belongs to.
	Parent objects.Ref `json:"parent"`
	// Listener is "" when the index does not place the routes on a listener.
	Listener string        `json:"listener,omitempty"`
	Routes   []objects.Ref `json:"routes"`
}

// RouteLink is one parentRef of a route to the described object that is not accepted.
type RouteLink struct {
	Route      objects.Ref       `json:"route"`
	ParentRef  objects.ParentRef `json:"parentRef"`
	Attachment string            `json:"attachment"`
	// Condition is the library's deciding condition; Pending is why the status is not current.
	Condition *Condition `json:"condition,omitempty"`
	Pending   string     `json:"pending,omitempty"`
}

// Condition is a condition's deciding fields.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// DelegationChild is a child route; Declared marks a candidate with no reported edge.
type DelegationChild struct {
	objects.Ref
	Declared bool `json:"declared,omitempty"`
}

// Backend is a backend a route sends or mirrors to.
type Backend struct {
	objects.Ref
	Mirror bool `json:"mirror,omitempty"`
}

// DeclaredRefs are the references a spec makes, not resolved.
type DeclaredRefs struct {
	ParentRefs  []objects.ParentRef `json:"parentRefs,omitempty"`
	BackendRefs []Backend           `json:"backendRefs,omitempty"`
}

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
	v.Relations = relations(r, f, k, u)
	v.Incomplete = incomplete(r, k)
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
		return &SpecSummary{Parent: &lv.Parent, Listeners: listenerSpecs(lv.Listeners)}
	case k.Group == objects.GatewayAPIGroup && k.Kind == "HTTPRoute":
		hv := objects.DecodeHTTPRoute(u)
		s := &SpecSummary{Hostnames: hv.Hostnames}
		for _, rl := range hv.Rules {
			s.Rules = append(s.Rules, ruleSpec(rl))
		}
		return s
	}
	return nil
}

func listenerSpecs(ls []objects.Listener) []ListenerSpec {
	out := make([]ListenerSpec, 0, len(ls))
	for _, l := range ls {
		s := ListenerSpec{Name: l.Name, Port: l.Port, Protocol: l.Protocol, Hostname: l.Hostname,
			AllowedRoutes: AllowedRoutes{From: l.AllowedRoutes.Namespaces.From, Kinds: l.AllowedRoutes.Kinds}}
		if sel := l.AllowedRoutes.Namespaces.Selector; sel != nil {
			s.AllowedRoutes.Selector = metav1.FormatLabelSelector(sel)
		}
		if l.TLS != nil {
			s.TLSMode = l.TLS.Mode
		}
		out = append(out, s)
	}
	return out
}

func keyMatches(in []objects.KeyMatch) []KeyMatch {
	var out []KeyMatch
	for _, k := range in {
		out = append(out, KeyMatch{Type: k.Type, Name: k.Name, Value: k.Value})
	}
	return out
}

func ruleSpec(rl objects.Rule) RuleSpec {
	s := RuleSpec{Name: rl.Name}
	for _, m := range rl.Matches {
		ms := MatchSpec{Headers: keyMatches(m.Headers), QueryParams: keyMatches(m.QueryParams), Method: m.Method}
		if m.Path != nil {
			ms.Path = &ValueMatch{Type: m.Path.Type, Value: m.Path.Value}
		}
		s.Matches = append(s.Matches, ms)
	}
	for _, fl := range rl.Filters {
		fs := FilterSpec{Type: fl.Type, Target: fl.ExtensionRef}
		if fl.Mirror != nil {
			fs.Target = &fl.Mirror.Backend
		}
		s.Filters = append(s.Filters, fs)
	}
	for _, b := range rl.BackendRefs {
		bs := BackendRefSpec{Ref: &b.Ref, Port: b.Port}
		if !b.WeightDefaulted {
			bs.Weight = new(b.Weight)
		}
		if d := b.Delegation; d != nil {
			bs = BackendRefSpec{Delegation: &DelegationSpec{Mode: string(d.Mode), Namespace: d.Namespace, Name: d.Name, Label: d.Label}}
		}
		s.BackendRefs = append(s.BackendRefs, bs)
	}
	return s
}

// conditionBlocks returns the object's conditions, then one block per listener, parent or ancestor entry.
func conditionBlocks(k kinds.Served, u *unstructured.Unstructured) []ConditionBlock {
	out := []ConditionBlock{}
	if cs := objects.Conditions(u); len(cs) > 0 || k.Category != kinds.CategoryRoute && k.Category != kinds.CategoryPolicy {
		out = append(out, ConditionBlock{Scope: ScopeObject, Conditions: conditionRows(u, cs)})
	}
	switch k.Category {
	case kinds.CategoryGateway, kinds.CategoryListenerSet:
		for _, l := range objects.GatewayListeners(u) {
			out = append(out, ConditionBlock{Scope: ScopeListener, Listener: l.Name, Conditions: conditionRows(u, l.Conditions)})
		}
	case kinds.CategoryRoute:
		for _, p := range objects.RouteParents(u) {
			out = append(out, ConditionBlock{Scope: ScopeParent, Ref: &p.ParentRef, Controller: p.ControllerName, Conditions: conditionRows(u, p.Conditions)})
		}
	case kinds.CategoryPolicy:
		for _, a := range objects.PolicyAncestors(u) {
			out = append(out, ConditionBlock{Scope: ScopeAncestor, Ref: &a.AncestorRef, Controller: a.ControllerName, Conditions: conditionRows(u, a.Conditions)})
		}
	}
	return out
}

func conditionRows(u *unstructured.Unstructured, cs []metav1.Condition) []ConditionRow {
	out := make([]ConditionRow, 0, len(cs))
	for _, c := range cs {
		row := ConditionRow{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			ObservedGeneration: c.ObservedGeneration, Stale: StaleCondition(u, c)}
		if !c.LastTransitionTime.IsZero() {
			row.LastTransition = c.LastTransitionTime.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

// ParentName is a parent or ancestor ref as "Kind/[namespace/]name[:section][:port]", seen from ns.
func ParentName(p objects.ParentRef, ns string) string {
	s := RefName(p.Ref(), ns, "")
	if p.SectionName != "" {
		s += ":" + p.SectionName
	}
	if p.Port != 0 {
		s += fmt.Sprintf(":%d", p.Port)
	}
	return s
}

// relations resolves u's relations from the owning install's index, or falls back to what u declares.
func relations(r *load.Result, f *Filter, k kinds.Served, u *unstructured.Unstructured) Relations {
	var rel Relations
	switch k.Category {
	case kinds.CategoryPolicy:
		return Relations{Of: RelationsPolicy, Ancestors: PolicyAncestors(f, u, "Accepted")}
	case kinds.CategoryGateway:
		rel.Of = RelationsGateway
	case kinds.CategoryListenerSet:
		rel.Of = RelationsListenerSet
	case kinds.CategoryRoute:
		rel.Of = RelationsRoute
	default:
		return rel
	}
	// Resolved stays nil while index inputs are down: nobody knows (Ruling R33).
	if f.indexDown != "" {
		rel.Unknown = f.indexDown
		// Parent states come from status; RouteParents degrades the index-only states (Ruling R33).
		if k.Category == kinds.CategoryRoute {
			rel.Parents, rel.DuplicateParents = RouteParents(r, f, u, "Accepted"), duplicateParents(u)
		}
		return rel
	}
	resolved := false
	rel.Resolved = &resolved
	ref := objects.RefOf(u)
	var ix *explore.Index
	if i, ok := f.installOfRef(ref); ok {
		ix = r.Model.Installs[i].Index
	}
	switch {
	case ix == nil:
	case k.Category == kinds.CategoryGateway:
		if id, ok := ix.FindGateway(ref); ok {
			gw := ix.Gateways[id]
			resolved = true
			rel.AttachedRoutes, rel.NotAccepted, rel.Pending = listenerRoutes(ix, gw.Routes, ref, gw.View.Listeners)
			for _, l := range gw.ListenerSets {
				rel.ListenerSets = append(rel.ListenerSets, ix.ListenerSets[l].Ref)
			}
		}
	case k.Category == kinds.CategoryListenerSet:
		if id, ok := ix.FindListenerSet(ref); ok {
			ls := ix.ListenerSets[id]
			resolved = true
			rel.AttachedRoutes, rel.NotAccepted, rel.Pending = listenerRoutes(ix, ls.Routes, ref, ls.View.Listeners)
		}
	default:
		if id, ok := ix.Row(ref); ok {
			resolved = true
			rel.Parents, rel.DuplicateParents = RouteParents(r, f, u, "Accepted"), duplicateParents(u)
			rel.DelegationParents = ix.Routes[id].ParentRoutes
			for _, c := range ix.ByParentRoute[ref] {
				rel.DelegationChildren = append(rel.DelegationChildren, DelegationChild{Ref: ix.Routes[c].Ref})
			}
			for _, c := range ix.Declared[ref] {
				rel.DelegationChildren = append(rel.DelegationChildren, DelegationChild{Ref: ix.Routes[c].Ref, Declared: true})
			}
			rel.Backends = backendList(k, u)
		}
	}
	if resolved {
		return rel
	}
	// Unresolved: only what the spec declares (design §3.2); a Gateway declares no references.
	switch k.Category {
	case kinds.CategoryListenerSet:
		rel.Declared = &DeclaredRefs{ParentRefs: []objects.ParentRef{objects.ListenerSetParent(u)}}
	case kinds.CategoryRoute:
		rel.Declared = &DeclaredRefs{ParentRefs: objects.RouteParentRefs(u), BackendRefs: backendList(k, u)}
	}
	return rel
}

// incomplete lists the inputs that are not ok among those k's output depends on: ownership
// inputs for an attributed kind, index inputs for a Gateway, ListenerSet or route.
func incomplete(r *load.Result, k kinds.Served) []load.Input {
	var gvrs []schema.GroupVersionResource
	switch {
	case k.Category == kinds.CategoryGateway, k.Category == kinds.CategoryListenerSet, k.Category == kinds.CategoryRoute:
		gvrs = indexInputs(r.Served)
	case attributed(k.Category):
		gvrs = ownershipInputs(r.Served)
	}
	var in []load.Input
	for _, gvr := range gvrs {
		if x, ok := r.Inputs[gvr]; ok && x.State != load.StateOK && !slices.ContainsFunc(in, func(y load.Input) bool { return y.Resource == x.Resource }) {
			in = append(in, x)
		}
	}
	slices.SortFunc(in, func(a, b load.Input) int { return cmp.Compare(a.Resource, b.Resource) })
	return in
}

// listenerKey is one listener of a Gateway or ListenerSet; listener "" is not known.
type listenerKey struct {
	parent   objects.Ref
	listener string
}

// listenerRoutes sorts the links of rows ids to target: accepted ones by listener,
// rejected ones as not accepted, the rest as pending (Ruling R30).
func listenerRoutes(ix *explore.Index, ids []explore.RowID, target objects.Ref, spec []objects.Listener) ([]ListenerRoutes, []RouteLink, []RouteLink) {
	attached := map[listenerKey][]objects.Ref{}
	var notAccepted, pending []RouteLink
	for _, id := range ids {
		rr := ix.Routes[id]
		for _, l := range rr.Parents {
			if l.Target != target && (l.Via == nil || *l.Via != target) {
				continue
			}
			if l.Attachment == explore.AttachmentAccepted {
				for _, li := range linkListeners(ix, id, l) {
					k := listenerKey{l.Target, li}
					if !slices.Contains(attached[k], rr.Ref) {
						attached[k] = append(attached[k], rr.Ref)
					}
				}
				continue
			}
			rl := RouteLink{Route: rr.Ref, ParentRef: l.Ref, Attachment: string(l.Attachment), Pending: l.Pending}
			if c := l.Cond; c != nil {
				rl.Condition = &Condition{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message}
			}
			if l.Attachment == explore.AttachmentNotAccepted {
				notAccepted = append(notAccepted, rl)
			} else {
				pending = append(pending, rl)
			}
		}
	}
	// The target's own listeners first, in spec order; then by parent and name, unknown last.
	rank := func(k listenerKey) int {
		if k.parent == target {
			if i := slices.IndexFunc(spec, func(l objects.Listener) bool { return l.Name == k.listener }); i >= 0 {
				return i
			}
		}
		return len(spec)
	}
	keys := make([]listenerKey, 0, len(attached))
	for k := range attached {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b listenerKey) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), a.parent.Compare(b.parent), cmp.Compare(boolInt(a.listener == ""), boolInt(b.listener == "")), cmp.Compare(a.listener, b.listener))
	})
	var out []ListenerRoutes
	for _, k := range keys {
		out = append(out, ListenerRoutes{Parent: k.parent, Listener: k.listener, Routes: attached[k]})
	}
	return out, notAccepted, pending
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// linkListeners names the listeners link l lands on: its sectionName, else the index's computed hits, else "".
func linkListeners(ix *explore.Index, id explore.RowID, l explore.ParentLink) []string {
	if l.Ref.SectionName != "" {
		return []string{l.Ref.SectionName}
	}
	var out []string
	if fs := ix.Facts(id, l); fs != nil {
		for _, h := range fs.Listeners {
			out = append(out, h.Listener)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// duplicateParents lists the spec parentRefs written more than once after defaults (design §3.1).
func duplicateParents(u *unstructured.Unstructured) []objects.ParentRef {
	ps := objects.RouteParentRefs(u)
	var out []objects.ParentRef
	for i, p := range ps {
		if slices.Contains(ps[:i], p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// backendList lists route u's distinct backends; delegation refs are not backends.
func backendList(k kinds.Served, u *unstructured.Unstructured) []Backend {
	var out []Backend
	for _, t := range backendTargets(k, u) {
		if b := (Backend{Ref: t.Ref, Mirror: t.Mirror}); !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}
