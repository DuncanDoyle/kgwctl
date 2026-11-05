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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
)

// Table is one get table: a kind's headers and rows (design §3.1).
type Table struct {
	Title   string
	Headers []string
	Rows    [][]string
	// Indexed is set when a cell depends on the explore index inputs (design §6.3).
	Indexed bool
}

// Cells with no value to show.
const (
	cellNone = "<none>"
	// cellNotIndexed marks an index-derived cell of an object no install indexes.
	cellNotIndexed = "-"
)

// NotFoundError reports that a named object is not among the visible objects of a kind.
type NotFoundError struct{ Resource, Name string }

// Error implements error.
func (e *NotFoundError) Error() string { return fmt.Sprintf("%s %q not found", e.Resource, e.Name) }

// GetTables returns one table per target and its objects for -o yaml/json, AGE measured at now.
// A name selects that object past the hiding rule; a target without it is a NotFoundError.
func GetTables(r *load.Result, f *Filter, targets []resolve.Target, wide bool, name string, now time.Time) ([]Table, []*unstructured.Unstructured, error) {
	if err := f.pinErr(); err != nil {
		return nil, nil, err
	}
	g := getter{r: r, f: f, wide: wide, now: now, multi: len(targets) > 1, install: len(r.Model.Installs) > 1}
	var tables []Table
	var objs []*unstructured.Unstructured
	for _, t := range targets {
		rows, err := g.rows(t, name)
		if err != nil {
			return nil, nil, err
		}
		c := category(t)
		tb := Table{Title: t.Label, Headers: g.headers(t), Indexed: c == kinds.CategoryGateway || c == kinds.CategoryRoute}
		for _, row := range rows {
			tb.Rows = append(tb.Rows, g.cells(t, row))
			objs = append(objs, row.u)
		}
		tables = append(tables, tb)
	}
	return tables, objs, nil
}

// getter holds what every cell of one get needs.
type getter struct {
	r   *load.Result
	f   *Filter
	now time.Time
	// wide adds the wide columns, multi prefixes names with the kind, install adds INSTALL.
	wide, multi, install bool
}

// row is one object of a target.
type row struct {
	k kinds.Served
	u *unstructured.Unstructured
}

// rows returns the objects of t that the get shows, sorted by namespace, name and group.
func (g getter) rows(t resolve.Target, name string) ([]row, error) {
	var out []row
	for _, k := range t.Kinds {
		in, ok := g.r.Inputs[k.GVR()]
		if !ok || in.State != load.StateOK {
			return nil, unreadable(in, k)
		}
		for _, u := range g.r.Model.Snapshot.Objects[k.GVR()] {
			if name != "" && u.GetName() != name {
				continue
			}
			if name != "" && g.f.inScope(u) || name == "" && g.f.Visible(k, u) {
				out = append(out, row{k, u})
			}
		}
	}
	if name != "" && len(out) == 0 {
		return nil, &NotFoundError{Resource: resourceOf(t), Name: name}
	}
	slices.SortFunc(out, func(a, b row) int {
		return cmp.Or(cmp.Compare(a.u.GetNamespace(), b.u.GetNamespace()), cmp.Compare(a.u.GetName(), b.u.GetName()), cmp.Compare(a.k.Group, b.k.Group))
	})
	return out, nil
}

// unreadable is the error for a requested kind whose input is not ok (design §6.3).
func unreadable(in load.Input, k kinds.Served) error {
	if in.Resource == "" {
		return fmt.Errorf("cannot read %s.%s: not loaded", k.Resource, k.Group)
	}
	if in.Reason == "" {
		return fmt.Errorf("cannot read %s: %s", in.Resource, in.State)
	}
	return fmt.Errorf("cannot read %s: %s: %s", in.Resource, in.State, in.Reason)
}

// resourceOf names a target in errors: its kind's plural, "listenersets" for the alias.
func resourceOf(t resolve.Target) string {
	if t.Alias {
		return strings.ToLower(t.Label) + "s"
	}
	return t.Kinds[0].Resource
}

// category is the column set of t: its kinds share one, the alias included.
func category(t resolve.Target) kinds.Category { return t.Kinds[0].Category }

// attributed reports whether the library assigns objects of c to installs (design §6.2).
func attributed(c kinds.Category) bool {
	switch c {
	case kinds.CategoryGatewayClass, kinds.CategoryGateway, kinds.CategoryListenerSet, kinds.CategoryRoute, kinds.CategoryPolicy:
		return true
	}
	return false
}

// headers returns t's columns as design §3.1 lists them.
func (g getter) headers(t resolve.Target) []string {
	var h []string
	if t.Kinds[0].Namespaced {
		h = append(h, "NAMESPACE")
	}
	h = append(h, "NAME")
	if t.Alias {
		h = append(h, "GROUP")
	}
	var mid, wide []string
	switch category(t) {
	case kinds.CategoryGatewayClass:
		mid, wide = []string{"CONTROLLER", "ACCEPTED"}, []string{"PARAMETERS"}
	case kinds.CategoryGateway:
		mid, wide = []string{"CLASS", "ADDRESS", "PROGRAMMED", "LISTENERS", "ROUTES"}, []string{"LISTENERSETS"}
	case kinds.CategoryListenerSet:
		mid = []string{"PARENT", "ACCEPTED", "PROGRAMMED", "LISTENERS"}
	case kinds.CategoryRoute:
		mid, wide = []string{"HOSTNAMES", "PARENTS", "ACCEPTED", "RESOLVED"}, []string{"BACKENDS", "DELEGATION"}
	case kinds.CategoryPolicy:
		mid = []string{"ANCESTORS", "ACCEPTED"}
	default:
		mid = []string{"CONDITIONS"}
	}
	h = append(append(h, mid...), "AGE")
	if g.wide {
		h = append(h, wide...)
	}
	if g.install && attributed(category(t)) {
		h = append(h, "INSTALL")
	}
	return h
}

// cells returns one row's values in the order of headers.
func (g getter) cells(t resolve.Target, rw row) []string {
	u := rw.u
	var c []string
	if t.Kinds[0].Namespaced {
		c = append(c, u.GetNamespace())
	}
	n := u.GetName()
	switch {
	// The GROUP column carries the group, so the prefix needs only the kind.
	case g.multi && t.Alias:
		n = strings.ToLower(rw.k.Kind) + "/" + n
	case g.multi:
		n = strings.ToLower(rw.k.Kind) + "." + rw.k.Group + "/" + n
	}
	c = append(c, n)
	if t.Alias {
		c = append(c, rw.k.Group)
	}
	var mid, wide []string
	switch category(t) {
	case kinds.CategoryGatewayClass:
		mid = []string{objects.ControllerName(u), ObjectCondition(u, "Accepted")}
		wide = []string{parametersRef(u)}
	case kinds.CategoryGateway:
		routes, lsets := g.gatewayRelations(u)
		mid = []string{objects.GatewayClassName(u), orNone(strings.Join(objects.GatewayAddresses(u), ",")),
			ObjectCondition(u, "Programmed"), strconv.Itoa(len(objects.GatewaySpecListeners(u))), routes}
		wide = []string{lsets}
	case kinds.CategoryListenerSet:
		p := objects.ListenerSetParent(u)
		mid = []string{refName(p.Ref(), u.GetNamespace(), "Gateway"), ObjectCondition(u, "Accepted"),
			ObjectCondition(u, "Programmed"), strconv.Itoa(listenerCount(u))}
	case kinds.CategoryRoute:
		mid = []string{hostnames(u), parentNames(objects.RouteParentRefs(u), u.GetNamespace()),
			Summarize(RouteParents(g.r, g.f, u, "Accepted")), Summarize(RouteParents(g.r, g.f, u, "ResolvedRefs"))}
		wide = []string{backends(rw.k, u), g.delegation(u)}
	case kinds.CategoryPolicy:
		ps := PolicyAncestors(g.f, u, "Accepted")
		var as []objects.ParentRef
		for _, a := range ps {
			as = append(as, a.Parent)
		}
		mid = []string{parentNames(as, u.GetNamespace()), Summarize(ps)}
	default:
		mid = []string{conditions(u)}
	}
	c = append(append(c, mid...), duration.HumanDuration(g.now.Sub(u.GetCreationTimestamp().Time)))
	if g.wide {
		c = append(c, wide...)
	}
	if g.install && attributed(category(t)) {
		c = append(c, g.installCell(u))
	}
	return c
}

// installCell lists the owning installs, sorted; "" without an owner, unknown while ownership is (R17).
func (g getter) installCell(u *unstructured.Unstructured) string {
	if !g.f.known {
		return "unknown"
	}
	var out []string
	for _, i := range g.f.owners[objects.RefOf(u)] {
		out = append(out, g.r.Model.Installs[i].Resolved.Display())
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// index finds ref's row in the owning install's explore index, else in the first index that has it.
func (g getter) index(find func(*explore.Index) (explore.RowID, bool), ref objects.Ref) (*explore.Index, explore.RowID, bool) {
	installs := g.r.Model.Installs
	order := make([]int, 0, len(installs)+1)
	if i, ok := g.f.installOfRef(ref); ok {
		order = append(order, i)
	}
	for i := range installs {
		order = append(order, i)
	}
	for _, i := range order {
		if ix := installs[i].Index; ix != nil {
			if id, ok := find(ix); ok {
				return ix, id, true
			}
		}
	}
	return nil, 0, false
}

// gatewayRelations returns the ROUTES and LISTENERSETS cells of Gateway u from the explore index.
func (g getter) gatewayRelations(u *unstructured.Unstructured) (routes, lsets string) {
	if down := g.f.IndexUnavailable(); down != "" {
		s := "unknown (" + down + ")"
		return s, s
	}
	ref := objects.RefOf(u)
	ix, id, ok := g.index(func(ix *explore.Index) (explore.RowID, bool) { return ix.FindGateway(ref) }, ref)
	if !ok {
		return cellNotIndexed, cellNotIndexed
	}
	gw := ix.Gateways[id]
	routes = strconv.Itoa(len(gw.Routes))
	if n := len(gw.NotAccepted); n > 0 {
		routes += fmt.Sprintf(" (%d not accepted)", n)
	}
	return routes, strconv.Itoa(len(gw.ListenerSets))
}

// delegation is the DELEGATION cell of route u: its reported parent routes and child count.
func (g getter) delegation(u *unstructured.Unstructured) string {
	if down := g.f.IndexUnavailable(); down != "" {
		return "unknown (" + down + ")"
	}
	ref := objects.RefOf(u)
	ix, id, ok := g.index(func(ix *explore.Index) (explore.RowID, bool) { return ix.Row(ref) }, ref)
	if !ok {
		return cellNotIndexed
	}
	rr := ix.Routes[id]
	var parts []string
	for _, p := range rr.ParentRoutes {
		parts = append(parts, "child of "+refName(p, u.GetNamespace(), "HTTPRoute"))
	}
	switch n := rr.Children; {
	case n == 1:
		parts = append(parts, "1 child")
	case n > 1:
		parts = append(parts, fmt.Sprintf("%d children", n))
	}
	return orNone(strings.Join(parts, ", "))
}

// refName is ref seen from namespace ns, "[Kind/][namespace/]name"; Kind is omitted when it is defKind.
func refName(ref objects.Ref, ns, defKind string) string {
	s := ref.Name
	if ref.Namespace != "" && ref.Namespace != ns {
		s = ref.Namespace + "/" + s
	}
	if ref.Kind != defKind {
		s = ref.Kind + "/" + s
	}
	return s
}

// parentNames lists parent refs as seen from ns, with ":section" and ":port" when set.
func parentNames(ps []objects.ParentRef, ns string) string {
	var out []string
	for _, p := range ps {
		s := refName(p.Ref(), ns, "Gateway")
		if p.SectionName != "" {
			s += ":" + p.SectionName
		}
		if p.Port != 0 {
			s += ":" + strconv.Itoa(int(p.Port))
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return orNone(strings.Join(out, ","))
}

// hostnames is spec.hostnames, or "*" when it matches every host.
func hostnames(u *unstructured.Unstructured) string {
	hs := objects.DecodeRouteHostnames(u).Hostnames
	if len(hs) == 0 {
		return "*"
	}
	return strings.Join(hs, ",")
}

// backends lists the distinct backends route u sends or mirrors to; delegation refs are not backends.
func backends(k kinds.Served, u *unstructured.Unstructured) string {
	var ts []objects.BackendTarget
	if k.Group == objects.GatewayAPIGroup && k.Kind == "HTTPRoute" {
		ts = objects.BackendTargets(objects.DecodeHTTPRoute(u))
	} else {
		ts = objects.RouteBackendTargets(u)
	}
	var out []string
	for _, t := range ts {
		if s := refName(t.Ref, u.GetNamespace(), "Service"); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return orNone(strings.Join(out, ","))
}

// parametersRef is a GatewayClass's spec.parametersRef as "Kind/[namespace/]name".
func parametersRef(u *unstructured.Unstructured) string {
	m, _, _ := unstructured.NestedStringMap(u.Object, "spec", "parametersRef")
	if m["name"] == "" {
		return cellNone
	}
	s := m["name"]
	if m["namespace"] != "" {
		s = m["namespace"] + "/" + s
	}
	return m["kind"] + "/" + s
}

// listenerCount is the length of spec.listeners.
func listenerCount(u *unstructured.Unstructured) int {
	ls, _, _ := unstructured.NestedSlice(u.Object, "spec", "listeners")
	return len(ls)
}

// conditions is every status condition as "Type=<cell>", in status order.
func conditions(u *unstructured.Unstructured) string {
	var out []string
	for _, c := range objects.Conditions(u) {
		out = append(out, c.Type+"="+ObjectCondition(u, c.Type))
	}
	if len(out) == 0 {
		return cellNoStatus
	}
	return strings.Join(out, ",")
}

func orNone(s string) string {
	if s == "" {
		return cellNone
	}
	return s
}
