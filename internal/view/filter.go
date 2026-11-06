package view

import (
	"fmt"
	"slices"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/engine"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/objects"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// Scope is what a command selects: namespaces, labels, install and controllers (design §6).
type Scope struct {
	// Namespace limits namespaced objects to one namespace; "" means all.
	Namespace     string
	AllNamespaces bool
	// Install is the --install value: an install ID or namespace/name.
	Install        string
	AllControllers bool
	// Selector filters by labels; nil matches everything.
	Selector labels.Selector
}

// Filter applies a Scope and the hiding rule of design §6.2 to one load.
// It is read-only after NewFilter, so it is safe for concurrent use.
type Filter struct {
	r     *load.Result
	scope Scope
	// hide is false with --all-controllers, without an install, or with an ownership input down.
	hide bool
	// known is false when an ownership input is down (design §6.3).
	known bool
	// indexDown names the index inputs that are not ok (design §6.3).
	indexDown string
	// selected is the --install install's index in r.Model.Installs, or -1.
	selected int
	// pin is set when --install selects no detected install.
	pin  error
	ours map[string]bool
	// owners maps each attributed object to the indices of the installs owning it.
	owners map[objects.Ref][]int
	// classes are the GatewayClasses by name.
	classes map[string]*unstructured.Unstructured
	// hidden holds the decision for every GatewayClass, Gateway, ListenerSet and route.
	hidden map[objects.Ref]bool
}

// NewFilter returns the Filter of s over r, and stderr warnings saying why
// nothing is hidden. With s.AllControllers, only unreadable inputs are warned about.
func NewFilter(r *load.Result, s Scope) (*Filter, []string) {
	f := &Filter{r: r, scope: s, selected: -1, ours: map[string]bool{}, owners: map[objects.Ref][]int{},
		classes: map[string]*unstructured.Unstructured{}, hidden: map[objects.Ref]bool{}}
	var warns []string
	// --all-controllers drops the hiding explanation, never an unreadable input.
	warn := func(input bool, format string, a ...any) {
		switch msg := fmt.Sprintf(format, a...); {
		case !s.AllControllers:
			warns = append(warns, msg+"; showing objects of all controllers")
		case input:
			warns = append(warns, msg)
		}
	}
	installs := r.Model.Installs
	down := unavailable(r, ownershipInputs(r.Served))
	f.known = down == ""
	f.indexDown = unavailable(r, indexInputs(r.Served))
	pinned, err := install.ParsePinned(s.Install)
	if err != nil {
		warn(false, "%v", err)
	} else if pinned != "" {
		f.selected = slices.IndexFunc(installs, func(im engine.InstallModel) bool { return im.Resolved.ID() == pinned })
	}
	for i, im := range installs {
		if f.selected < 0 || i == f.selected {
			for _, c := range im.Resolved.ControllerNames {
				f.ours[c] = true
			}
		}
		if im.Owned != nil {
			f.addOwned(i, im.Owned)
		}
	}
	pinMissing := s.Install != "" && f.selected < 0
	if pinMissing {
		f.pin = fmt.Errorf("install %q not detected", s.Install)
		if err != nil {
			f.pin = fmt.Errorf("invalid --install %q: %w", s.Install, err)
		}
	}
	switch {
	case !f.known:
		warn(true, "ownership unknown (%s)", down)
	case len(installs) == 0:
		warn(false, "no kgateway install detected")
	case pinMissing && err == nil:
		warn(false, "install %q not detected", s.Install)
	}
	f.hide = !s.AllControllers && f.known && len(installs) > 0 && !pinMissing
	if f.hide {
		f.decideParents()
	}
	return f, warns
}

// ownershipInputs are the inputs ownership depends on (design §6.3).
func ownershipInputs(served []kinds.Served) []schema.GroupVersionResource {
	out := []schema.GroupVersionResource{cache.NamespacesGVR, cache.DeploymentsGVR, cache.PodsGVR}
	for _, c := range []kinds.Category{kinds.CategoryGatewayClass, kinds.CategoryGateway, kinds.CategoryListenerSet} {
		for _, k := range kinds.ByCategory(served, c) {
			out = append(out, k.GVR())
		}
	}
	return out
}

func (f *Filter) addOwned(i int, o *install.Owned) {
	add := func(u *unstructured.Unstructured) {
		ref := objects.RefOf(u)
		if !slices.Contains(f.owners[ref], i) {
			f.owners[ref] = append(f.owners[ref], i)
		}
	}
	for _, us := range [][]*unstructured.Unstructured{o.GatewayClasses, o.Gateways, o.ListenerSets} {
		for _, u := range us {
			add(u)
		}
	}
	for _, oos := range [][]install.OwnedObject{o.Routes, o.Policies} {
		for _, oo := range oos {
			add(oo.Obj)
		}
	}
}

// parentCategories are the kinds a Gateway, ListenerSet or route decision may depend on.
var parentCategories = []kinds.Category{kinds.CategoryGatewayClass, kinds.CategoryGateway, kinds.CategoryListenerSet, kinds.CategoryRoute}

// decideParents decides every object of parentCategories, parents first.
func (f *Filter) decideParents() {
	type node struct {
		k kinds.Served
		u *unstructured.Unstructured
	}
	nodes := map[objects.Ref]node{}
	for _, c := range parentCategories {
		for _, k := range kinds.ByCategory(f.r.Served, c) {
			for _, u := range f.r.Model.Snapshot.Objects[k.GVR()] {
				nodes[objects.RefOf(u)] = node{k, u}
				if c == kinds.CategoryGatewayClass {
					f.classes[u.GetName()] = u
				}
			}
		}
	}
	// A parent on a parent-route cycle counts as not hidden.
	visiting := map[objects.Ref]bool{}
	var hidden func(ref objects.Ref) bool
	hidden = func(ref objects.Ref) bool {
		if h, ok := f.hidden[ref]; ok {
			return h
		}
		n, ok := nodes[ref]
		if !ok || visiting[ref] {
			return false
		}
		visiting[ref] = true
		h := f.decide(n.k.Category, n.u, hidden)
		delete(visiting, ref)
		f.hidden[ref] = h
		return h
	}
	for ref := range nodes {
		hidden(ref)
	}
}

// decide applies the design §6.2 table to u; parentHidden decides an existing parent by reference.
func (f *Filter) decide(c kinds.Category, u *unstructured.Unstructured, parentHidden func(objects.Ref) bool) bool {
	ref := objects.RefOf(u)
	switch c {
	case kinds.CategoryGatewayClass:
		return !f.ours[objects.ControllerName(u)] || f.foreign(ref)
	case kinds.CategoryGateway:
		gc, ok := f.classes[objects.GatewayClassName(u)]
		return ok && parentHidden(objects.RefOf(gc)) || f.foreign(ref)
	case kinds.CategoryListenerSet:
		p := objects.ListenerSetParent(u)
		return p.Name != "" && parentHidden(p.Ref())
	case kinds.CategoryRoute:
		// Spec parentRefs only: status from an old parent never hides a route.
		ps := objects.RouteParentRefs(u)
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p objects.ParentRef) bool { return !parentHidden(p.Ref()) })
	case kinds.CategoryPolicy:
		as := objects.PolicyAncestors(u)
		theirs := len(as) > 0 && !slices.ContainsFunc(as, func(a objects.AncestorStatus) bool { return f.ours[a.ControllerName] })
		return theirs || f.foreign(ref)
	}
	return false
}

// foreign reports whether --install selected an install and only other installs own ref.
func (f *Filter) foreign(ref objects.Ref) bool {
	o := f.owners[ref]
	return f.selected >= 0 && len(o) > 0 && !slices.Contains(o, f.selected)
}

// Visible reports whether get shows obj, an object of kind k: it is in the
// scope's namespace and labels, and the hiding rule does not hide it.
func (f *Filter) Visible(k kinds.Served, obj *unstructured.Unstructured) bool {
	if !f.inScope(obj) {
		return false
	}
	if !f.hide {
		return true
	}
	if h, ok := f.hidden[objects.RefOf(obj)]; ok {
		return !h
	}
	return !f.decide(k.Category, obj, func(ref objects.Ref) bool { return f.hidden[ref] })
}

// inScope reports whether obj is in the scope's namespace and matches its labels.
func (f *Filter) inScope(obj *unstructured.Unstructured) bool {
	if ns := f.scope.Namespace; ns != "" && !f.scope.AllNamespaces && obj.GetNamespace() != "" && obj.GetNamespace() != ns {
		return false
	}
	return f.scope.Selector == nil || f.scope.Selector.Matches(labels.Set(obj.GetLabels()))
}

// pinErr is the --install error of Ruling R16: set while ownership is known;
// otherwise NewFilter warned and hides nothing (design §6.3).
func (f *Filter) pinErr() error {
	if f.known {
		return f.pin
	}
	return nil
}

// PinIgnored is the warning for an --install that selects no detected install
// while ownership is unknown, so nothing is narrowed; "" otherwise.
func (f *Filter) PinIgnored() string {
	if f.pin == nil || f.known {
		return ""
	}
	return f.pin.Error() + "; showing every install"
}

// InstallOf returns the install owning obj: the --install one when it is an
// owner, else the first. It is false when no install owns obj or ownership is unknown.
func (f *Filter) InstallOf(obj *unstructured.Unstructured) (install.Resolved, bool) {
	i, ok := f.installOfRef(objects.RefOf(obj))
	if !ok {
		return install.Resolved{}, false
	}
	return f.r.Model.Installs[i].Resolved, true
}

// installOfRef is InstallOf by reference, as an index into r.Model.Installs.
func (f *Filter) installOfRef(ref objects.Ref) (int, bool) {
	o := f.owners[ref]
	if !f.known || len(o) == 0 {
		return 0, false
	}
	if slices.Contains(o, f.selected) {
		return f.selected, true
	}
	return o[0], true
}

// Ours reports whether controllerName is a controller of a detected install,
// or of the --install install when one is selected.
func (f *Filter) Ours(controllerName string) bool { return f.ours[controllerName] }
