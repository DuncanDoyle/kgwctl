package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// fixtureInstall is one kgateway controller Deployment in a scopeFixture.
type fixtureInstall struct {
	ns string
	// selectors is the KGW_DISCOVERY_NAMESPACE_SELECTORS value; "" sets none.
	selectors string
	envFrom   bool
}

// scopeFixture builds a load.Result the way Load does: Snellius detection,
// scope provider and Assign over a hand-made snapshot, without a cluster.
type scopeFixture struct {
	namespaces map[string]map[string]string
	installs   []fixtureInstall
	objects    string
	denied     []schema.GroupVersionResource
}

func (fx scopeFixture) result(t *testing.T) *load.Result {
	t.Helper()
	var served []kinds.Served
	for _, d := range kinds.Upstream() {
		served = append(served, kinds.Served{Descriptor: d, Version: d.Versions[0]})
	}
	denied := func(gvr schema.GroupVersionResource) bool { return slices.Contains(fx.denied, gvr) }
	snap := &cache.Snapshot{Objects: map[schema.GroupVersionResource][]*unstructured.Unstructured{}}
	for _, doc := range strings.Split(fx.objects, "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(served, func(k kinds.Served) bool { return k.Kind == u.GetKind() })
		if i < 0 {
			t.Fatalf("no served kind %s", u.GetKind())
		}
		if gvr := served[i].GVR(); !denied(gvr) {
			snap.Objects[gvr] = append(snap.Objects[gvr], u)
		}
	}
	if !denied(cache.NamespacesGVR) {
		for name, ls := range fx.namespaces {
			snap.Namespaces = append(snap.Namespaces, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: ls}})
		}
	}
	var resolved []install.Resolved
	for _, fi := range fx.installs {
		c := corev1.Container{Name: "kgateway", Image: "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0"}
		if fi.selectors != "" {
			c.Env = []corev1.EnvVar{{Name: install.DiscoveryNamespaceSelectorsEnv, Value: fi.selectors}}
		}
		if fi.envFrom {
			c.EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "env"}}}}
		}
		sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kgateway"}}
		d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: fi.ns, Name: "kgateway"},
			Spec: appsv1.DeploymentSpec{Selector: sel, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}}}
		snap.Deployments = append(snap.Deployments, d)
		in, ok := install.UpstreamDetector{}.Detect(d)
		if !ok {
			t.Fatal("install not detected")
		}
		resolved = append(resolved, install.Resolved{Install: in, Scope: install.NamespaceSelectorScope{}.Scope(in)})
	}
	asg := install.Assign(snap, served, resolved, nil)
	m := &engine.Model{Snapshot: snap, Served: served}
	for _, r := range resolved {
		m.Installs = append(m.Installs, engine.InstallModel{Resolved: r, Owned: asg.PerInstall[r.ID()]})
	}
	inputs := map[schema.GroupVersionResource]load.Input{}
	for _, gvr := range append([]schema.GroupVersionResource{cache.NamespacesGVR, cache.DeploymentsGVR, cache.PodsGVR}, gvrs(served)...) {
		st := load.StateOK
		if denied(gvr) {
			st = load.StateDenied
		}
		inputs[gvr] = load.Input{Resource: gvr.Resource, GVR: gvr, State: st}
	}
	return &load.Result{Model: m, Served: served, Inputs: inputs}
}

func gvrs(served []kinds.Served) []schema.GroupVersionResource {
	var out []schema.GroupVersionResource
	for _, k := range served {
		out = append(out, k.GVR())
	}
	return out
}

// find returns the fixture object of kind ns/name.
func find(t *testing.T, res *load.Result, kind, ns, name string) *unstructured.Unstructured {
	t.Helper()
	for _, us := range res.Model.Snapshot.Objects {
		for _, u := range us {
			if u.GetKind() == kind && u.GetNamespace() == ns && u.GetName() == name {
				return u
			}
		}
	}
	t.Fatalf("no %s %s/%s", kind, ns, name)
	return nil
}

const (
	kgwClass = `apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: kgateway}
spec: {controllerName: kgateway.dev/kgateway}
`
	otherClass = `apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: other}
spec: {controllerName: example.com/other}
`
)

func gateway(ns, name, class string) string {
	return "apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata: {namespace: " + ns + ", name: " + name +
		"}\nspec: {gatewayClassName: " + class + ", listeners: [{name: http, port: 80, protocol: HTTP}]}\n"
}

func docs(ds ...string) string { return strings.Join(ds, "\n---\n") }

// mustScope runs resolveScope and fails on error.
func mustScope(t *testing.T, f *Flags, explicit bool, res *load.Result) (pb.Scope, ib.GatewaySet, func(*unstructured.Unstructured) pb.ExcludeReason, []string) {
	t.Helper()
	sc, gws, keep, warns, err := resolveScope(f, explicit, false, res)
	if err != nil {
		t.Fatal(err)
	}
	return sc, gws, keep, warns
}

func TestResolveScope(t *testing.T) {
	nss := map[string]map[string]string{"kgateway-system": nil, "other-system": nil, "team-a": nil, "team-b": nil}
	one := scopeFixture{namespaces: nss, installs: []fixtureInstall{{ns: "kgateway-system"}}, objects: docs(kgwClass, otherClass, gateway("team-b", "foreign", "other"))}
	res := one.result(t)

	for _, tc := range []struct {
		name     string
		f        Flags
		explicit bool
		want     []string
	}{
		{"default", Flags{}, false, nil},
		{"-A", Flags{AllNamespaces: true}, false, nil},
		{"-n", Flags{Namespace: "team-a"}, true, []string{"team-a", "kgateway-system"}},
	} {
		sc, _, _, warns := mustScope(t, &tc.f, tc.explicit, res)
		if got := sc.Namespaces(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: namespaces %v, want %v", tc.name, got, tc.want)
		}
		if len(sc.Installs) != 1 || sc.Installs[0].Chosen != "detected" || sc.Installs[0].Deployment != "kgateway" || sc.Installs[0].MetricsPort != 9092 {
			t.Errorf("%s: installs %+v", tc.name, sc.Installs)
		}
		if len(warns) != 0 {
			t.Errorf("%s: warnings %q", tc.name, warns)
		}
	}

	sc, _, _, _ := mustScope(t, &Flags{Install: "kgateway-system/kgateway"}, false, res)
	if sc.Installs[0].Chosen != "flag" {
		t.Errorf("--install: chosen %q, want flag", sc.Installs[0].Chosen)
	}
	_, _, _, _, err := resolveScope(&Flags{Install: "x/y"}, false, false, res)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 2 {
		t.Errorf("--install x/y: err %v, want exit 2", err)
	}

	two := one
	two.installs = []fixtureInstall{{ns: "kgateway-system"}, {ns: "other-system"}}
	sc, _, _, warns := mustScope(t, &Flags{}, false, two.result(t))
	if len(sc.Installs) != 2 || sc.Installs[0].Chosen != "detected" || sc.Installs[1].Chosen != "detected" || len(warns) != 1 {
		t.Errorf("two installs: installs %+v, warnings %q; want both detected and one warning", sc.Installs, warns)
	}

	unknown := one
	unknown.denied = []schema.GroupVersionResource{{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}}
	ures := unknown.result(t)
	_, _, keep, warns := mustScope(t, &Flags{}, false, ures)
	if r := keep(find(t, ures, "Gateway", "team-b", "foreign")); r != pb.ExcludeNone || len(warns) != 1 {
		t.Errorf("ownership unknown: keep %q, warnings %q; want kept and one warning", r, warns)
	}

	none := one
	none.installs = nil
	sc, _, _, warns = mustScope(t, &Flags{}, false, none.result(t))
	if len(sc.Installs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "no kgateway install") {
		t.Errorf("no install: installs %+v, warnings %q", sc.Installs, warns)
	}
	if cs, errs := controllerSelectors(none.result(t), sc); len(cs) != 0 || len(errs) != 0 {
		t.Errorf("no install: controller selectors %v, errors %v", cs, errs)
	}

	// An install without a usable selector is named, not silently dropped.
	sc, _, _, _ = mustScope(t, &Flags{}, false, res)
	if cs, errs := controllerSelectors(res, sc); len(cs) != 1 || len(errs) != 0 {
		t.Errorf("controller selectors %v, errors %v; want one selector", cs, errs)
	}
	bad := one.result(t)
	bad.Model.Installs[0].Resolved.Deployment.Spec.Selector = &metav1.LabelSelector{}
	cs, errs := controllerSelectors(bad, sc)
	if len(cs) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "kgateway-system/kgateway") {
		t.Errorf("empty selector: selectors %v, errors %v; want one error naming the install", cs, errs)
	}
}

func TestResolveDiscoveryScope(t *testing.T) {
	const tp = `apiVersion: gateway.kgateway.dev/v1alpha1
kind: TrafficPolicy
metadata: {namespace: team-b, name: tp}
`
	fx := scopeFixture{
		namespaces: map[string]map[string]string{"kgateway-system": {"team": "a"}, "team-a": {"team": "a"}, "team-b": {"team": "b"}},
		installs:   []fixtureInstall{{ns: "kgateway-system", selectors: `[{"matchLabels":{"team":"a"}}]`}},
		objects:    docs(kgwClass, otherClass, gateway("team-a", "foreign", "other"), tp),
	}
	res := fx.result(t)
	keepOf := func(keep func(*unstructured.Unstructured) pb.ExcludeReason, kind, ns, name string) pb.ExcludeReason {
		return keep(find(t, res, kind, ns, name))
	}

	sc, _, keep, _ := mustScope(t, &Flags{}, false, res)
	d := sc.Installs[0].Discovery
	if d.Mode != "selectors" || !slices.Equal(d.Selectors, []string{"team=a"}) || !strings.HasPrefix(d.Source, "snellius M-4 (interim)") {
		t.Errorf("discovery %+v", d)
	}
	if !slices.Contains(d.Unwatched, "team-b") || slices.Contains(d.Unwatched, "team-a") {
		t.Errorf("unwatched %v, want team-b only of the teams", d.Unwatched)
	}
	for _, c := range []struct {
		kind, ns, name string
		want           pb.ExcludeReason
	}{
		{"TrafficPolicy", "team-b", "tp", pb.ExcludeScope},
		{"GatewayClass", "", "kgateway", pb.ExcludeNone},
		{"GatewayClass", "", "other", pb.ExcludeOwnership},
		{"Gateway", "team-a", "foreign", pb.ExcludeOwnership},
	} {
		if got := keepOf(keep, c.kind, c.ns, c.name); got != c.want {
			t.Errorf("keep %s %s/%s = %q, want %q", c.kind, c.ns, c.name, got, c.want)
		}
	}

	sc, _, keep, _ = mustScope(t, &Flags{AllControllers: true}, false, res)
	for _, o := range [][3]string{{"TrafficPolicy", "team-b", "tp"}, {"GatewayClass", "", "other"}, {"Gateway", "team-a", "foreign"}} {
		if got := keepOf(keep, o[0], o[1], o[2]); got != pb.ExcludeNone {
			t.Errorf("--all-controllers: keep %v = %q", o, got)
		}
	}
	if sc.Installs[0].Discovery.Mode != "selectors" {
		t.Errorf("--all-controllers: discovery %+v, want it recorded", sc.Installs[0].Discovery)
	}

	for _, c := range []struct {
		name string
		mod  func(*scopeFixture)
		mode string
	}{
		{"empty value", func(f *scopeFixture) { f.installs[0].selectors = "" }, "all"},
		{"envFrom only", func(f *scopeFixture) { f.installs[0] = fixtureInstall{ns: "kgateway-system", envFrom: true} }, "unknown"},
		{"namespaces denied", func(f *scopeFixture) { f.denied = []schema.GroupVersionResource{cache.NamespacesGVR} }, "namespaces-unreadable"},
	} {
		v := fx
		v.installs = slices.Clone(fx.installs)
		c.mod(&v)
		r := v.result(t)
		sc, _, keep, _ := mustScope(t, &Flags{}, false, r)
		d := sc.Installs[0].Discovery
		if d.Mode != c.mode || (c.mode != "all" && d.Reason == "") {
			t.Errorf("%s: discovery %+v, want mode %s with a reason", c.name, d, c.mode)
		}
		if got := keep(find(t, r, "TrafficPolicy", "team-b", "tp")); got != pb.ExcludeNone {
			t.Errorf("%s: team-b TrafficPolicy %q, want kept", c.name, got)
		}
	}
}

func TestResolveScopeUnion(t *testing.T) {
	const route = `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {namespace: team-b, name: route-b}
spec: {parentRefs: [{name: gw-a, namespace: team-a}]}
`
	fx := scopeFixture{
		namespaces: map[string]map[string]string{"a-sys": nil, "b-sys": nil, "team-a": {"watch-a": "true"},
			"team-b": {"watch-b": "true"}, "shared": {"watch-a": "true", "watch-b": "true"}},
		installs: []fixtureInstall{{ns: "a-sys", selectors: `[{"matchLabels":{"watch-a":"true"}}]`},
			{ns: "b-sys", selectors: `[{"matchLabels":{"watch-b":"true"}}]`}},
		objects: docs(kgwClass, gateway("team-a", "gw-a", "kgateway"), gateway("shared", "gw-shared", "kgateway"), route),
	}
	res := fx.result(t)
	rt := find(t, res, "HTTPRoute", "team-b", "route-b")
	shared := types.NamespacedName{Namespace: "shared", Name: "gw-shared"}

	_, gws, keep, _ := mustScope(t, &Flags{}, false, res)
	if got := keep(rt); got != pb.ExcludeNone {
		t.Errorf("union: route %q, want kept", got)
	}
	if owner, ok := gws[types.NamespacedName{Namespace: "team-a", Name: "gw-a"}]; !ok || owner != "a-sys.kgateway" {
		t.Errorf("union: gateways %v", gws)
	}

	_, gws, keep, _ = mustScope(t, &Flags{Install: "a-sys/kgateway"}, false, res)
	if got := keep(rt); got != pb.ExcludeScope {
		t.Errorf("--install a: route %q, want scope", got)
	}
	if got := keep(find(t, res, "Gateway", "shared", "gw-shared")); got != pb.ExcludeNone {
		t.Errorf("--install a: ambiguous Gateway %q, want kept", got)
	}
	if owner, ok := gws[shared]; !ok || owner != "" {
		t.Errorf("--install a: gateways %v, want the ambiguous one with no owner", gws)
	}
}

func TestResolveWorkloadNamespaces(t *testing.T) {
	const route = `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {namespace: team-a, name: r}
spec:
  parentRefs: [{name: gw}]
  rules: [{backendRefs: [{name: svc, namespace: team-c, port: 80}, {name: local, port: 80}]}]
`
	fx := scopeFixture{
		namespaces: map[string]map[string]string{"kgateway-system": nil, "team-a": nil, "team-b": nil, "team-c": nil},
		installs:   []fixtureInstall{{ns: "kgateway-system"}},
		objects:    docs(kgwClass, otherClass, gateway("team-a", "gw", "kgateway"), gateway("team-b", "foreign", "other"), route),
	}
	res := fx.result(t)
	for _, c := range []struct {
		name     string
		f        Flags
		explicit bool
		want     []string
	}{
		{"default", Flags{}, false, []string{"kgateway-system", "team-a", "team-c"}},
		{"-n team-a", Flags{Namespace: "team-a"}, true, []string{"kgateway-system", "team-a"}},
		{"-n team-a --all-controllers", Flags{Namespace: "team-a", AllControllers: true}, true, []string{"kgateway-system", "team-a"}},
		{"--all-controllers", Flags{AllControllers: true}, false, nil},
	} {
		sc, _, _, _ := mustScope(t, &c.f, c.explicit, res)
		if !slices.Equal(sc.WorkloadNamespaces, c.want) || (c.want != nil && sc.WorkloadNamespaces == nil) {
			t.Errorf("%s: workload namespaces %v, want %v", c.name, sc.WorkloadNamespaces, c.want)
		}
	}
	// Replica namespaces join, within the -n bound.
	sc, _, _, _ := mustScope(t, &Flags{}, false, res)
	sc = withReplicaNamespaces(sc, []pb.Replica{{Namespace: "team-d"}})
	if !slices.Contains(sc.WorkloadNamespaces, "team-d") {
		t.Errorf("replica namespace not added: %v", sc.WorkloadNamespaces)
	}
	sc, _, _, _ = mustScope(t, &Flags{Namespace: "team-a"}, true, res)
	if got := withReplicaNamespaces(sc, []pb.Replica{{Namespace: "team-d"}}).WorkloadNamespaces; slices.Contains(got, "team-d") {
		t.Errorf("-n team-a: replica namespace team-d added: %v", got)
	}
}
