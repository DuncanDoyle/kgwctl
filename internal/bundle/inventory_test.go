package bundle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// proxyPod is a kgateway proxy Pod of Gateway ns/gw, annotated unless gw is "".
func proxyPod(ns, name, gw string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name),
			Labels: map[string]string{ProxyLabel: ProxyLabelValue, gatewayNameLabel: shorten(gw)}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "kgateway-proxy"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if gw != "" {
		p.Annotations = map[string]string{GatewayFullNameAnnotation: gw}
	}
	return p
}

// shorten mimics the proxy Deployment's label for names over 63 characters.
func shorten(name string) string {
	if len(name) <= 63 {
		return name
	}
	return name[:50] + "-0123456789ab"
}

func gwKey(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}

func podNames(rs []pb.Replica) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Namespace+"/"+r.Pod)
	}
	slices.Sort(out)
	return out
}

func TestInventoryFullName(t *testing.T) {
	long := strings.Repeat("g", 70)
	objs := []runtime.Object{proxyPod("team-a", "p1", long), proxyPod("team-a", "p2", long), proxyPod("team-a", "p3", long)}
	// Same shortened label, but the annotation names another Gateway.
	other := proxyPod("team-a", "p4", long[:69]+"x")
	other.Labels[gatewayNameLabel] = shorten(long)
	objs = append(objs, other)
	kube := kubefake.NewClientset(objs...)
	rs, errs := Inventory(context.Background(), kube, pb.Scope{}, GatewaySet{gwKey("team-a", long): "kgateway-system.kgateway"}, nil)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/p1", "team-a/p2", "team-a/p3"}) {
		t.Fatalf("replicas %v, want p1..p3", got)
	}
	for _, r := range rs {
		if r.Identity != pb.IdentityAnnotation || r.Role != pb.RoleProxy || r.Owner != "team-a/"+long || r.UID == "" {
			t.Errorf("replica %+v, want an annotation-identified proxy of team-a/%s", r, long)
		}
	}
}

func TestInventoryUnverified(t *testing.T) {
	// The label names the Gateway, but only the annotation verifies it.
	bare := proxyPod("team-a", "bare", "")
	bare.Labels[gatewayNameLabel] = "gw"
	kube := kubefake.NewClientset(bare)
	rs, errs := Inventory(context.Background(), kube, pb.Scope{}, GatewaySet{gwKey("team-a", "gw"): ""}, nil)
	if len(rs) != 1 || rs[0].Identity != pb.IdentityUnverified {
		t.Fatalf("replicas %+v, want one unverified", rs)
	}
	if len(errs) != 1 {
		t.Fatalf("errors %v, want one line for the unverified Pod", errs)
	}
	var re *ReplicaError
	if !errors.As(errs[0], &re) || re.Source.Pod != "bare" || re.Path.String() != "proxies/team-a/bare" {
		t.Errorf("error %v, want a ReplicaError at proxies/team-a/bare", errs[0])
	}
	for _, a := range kube.Actions() {
		if a.GetResource().Resource != "pods" {
			t.Errorf("request for %s, want pods only", a.GetResource().Resource)
		}
	}
}

// TestInventoryUnverifiedShortened: a release without the annotation shortens
// the label of a long Gateway name; its Pods are still recorded unverified.
func TestInventoryUnverifiedShortened(t *testing.T) {
	long := strings.Repeat("g", 70)
	short := proxyPod("team-a", "short", "")
	short.Labels[gatewayNameLabel] = shorten(long)
	stray := proxyPod("team-a", "stray", "")
	stray.Labels[gatewayNameLabel] = "unrelated"
	noLabel := proxyPod("team-a", "no-proxy-label", "")
	delete(noLabel.Labels, ProxyLabel)
	kube := kubefake.NewClientset(short, stray, noLabel)
	gws := GatewaySet{gwKey("team-a", long): ""}

	rs, errs := Inventory(context.Background(), kube, pb.Scope{}, gws, nil)
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/short"}) || len(errs) != 1 || rs[0].Identity != pb.IdentityUnverified {
		t.Errorf("replicas %+v, errors %v; want short unverified", rs, errs)
	}
	// Within -n an unattributable proxy Pod is reported too; one without the proxy label never is.
	rs, errs = Inventory(context.Background(), kube, pb.Scope{Namespace: "team-a"}, gws, nil)
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/short", "team-a/stray"}) || len(errs) != 2 {
		t.Errorf("-n team-a: replicas %v, errors %v; want short and stray unverified", got, errs)
	}
	for _, a := range kube.Actions() {
		if a.GetResource().Resource != "pods" {
			t.Errorf("request for %s, want pods only", a.GetResource().Resource)
		}
	}
}

func TestInventoryStates(t *testing.T) {
	start := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	notReady := proxyPod("team-a", "not-ready", "gw")
	notReady.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	ready := proxyPod("team-a", "ready", "gw")
	ready.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	ready.Status.StartTime = &start
	term := proxyPod("team-a", "terminating", "gw")
	term.DeletionTimestamp = &start
	term.Finalizers = []string{"x"}
	pending := proxyPod("team-a", "pending", "gw")
	pending.Status.Phase = corev1.PodPending
	restarted := proxyPod("team-a", "restarted", "gw")
	restarted.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	restarted.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 2}}
	restarted.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "kgateway-proxy", RestartCount: 5}}
	kube := kubefake.NewClientset(notReady, ready, term, pending, restarted)
	rs, errs := Inventory(context.Background(), kube, pb.Scope{}, GatewaySet{gwKey("team-a", "gw"): ""}, nil)
	if len(errs) != 0 || len(rs) != 5 {
		t.Fatalf("replicas %v, errors %v; want all five", podNames(rs), errs)
	}
	by := map[string]pb.Replica{}
	for _, r := range rs {
		by[r.Pod] = r
	}
	if by["not-ready"].Ready || !by["ready"].Ready || !by["ready"].StartTime.Equal(start.Time) {
		t.Errorf("ready states: %+v / %+v", by["not-ready"], by["ready"])
	}
	if !by["terminating"].Terminating || by["pending"].Phase != corev1.PodPending {
		t.Errorf("terminating %+v, pending %+v", by["terminating"], by["pending"])
	}
	want := []pb.Container{{Name: "init", Init: true, Restarts: 2}, {Name: "kgateway-proxy", Restarts: 5}}
	if got := by["restarted"].Containers; !slices.Equal(got, want) {
		t.Errorf("containers %+v, want %+v", got, want)
	}
}

func TestInventoryMetricsPort(t *testing.T) {
	declared := proxyPod("team-a", "declared", "gw")
	declared.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 9091}}
	bare := proxyPod("team-a", "bare", "gw")
	ctrl := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kgateway-system", Name: "kgateway-0", Labels: map[string]string{"app": "kgateway"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kgateway", Ports: []corev1.ContainerPort{{ContainerPort: 9095}}}}},
	}
	other := ctrl.DeepCopy()
	other.Name, other.Labels = "unrelated", map[string]string{"app": "other"}
	kube := kubefake.NewClientset(declared, bare, ctrl, other)
	in := pb.Install{Namespace: "kgateway-system", Deployment: "kgateway", MetricsPort: 9095}
	ctrls := []ControllerSelector{{Install: in, Selector: labels.SelectorFromSet(labels.Set{"app": "kgateway"})}}
	rs, errs := Inventory(context.Background(), kube, pb.Scope{}, GatewaySet{gwKey("team-a", "gw"): ""}, ctrls)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	by := map[string]pb.Replica{}
	for _, r := range rs {
		by[r.Pod] = r
	}
	if r := by["declared"]; r.MetricsPort != 9091 || !r.MetricsPortDeclared {
		t.Errorf("declared: %+v", r)
	}
	if r := by["bare"]; r.MetricsPort != 9091 || r.MetricsPortDeclared {
		t.Errorf("bare: %+v", r)
	}
	c := by["kgateway-0"]
	if c.Role != pb.RoleController || c.Identity != pb.IdentitySelector || c.Owner != "kgateway-system/kgateway" ||
		c.MetricsPort != 9095 || !c.MetricsPortDeclared {
		t.Errorf("controller: %+v", c)
	}
	if _, ok := by["unrelated"]; ok {
		t.Error("a Pod outside the Deployment selector is a replica")
	}
}

func TestInventoryAllControllers(t *testing.T) {
	kept := proxyPod("team-a", "kept", "gw")
	foreign := proxyPod("team-a", "foreign", "other-class")
	outOfScope := proxyPod("team-b", "unwatched", "gw-b")
	unlabelled := proxyPod("team-a", "unlabelled", "gw")
	delete(unlabelled.Labels, ProxyLabel)
	kube := kubefake.NewClientset(kept, foreign, outOfScope, unlabelled)
	gws := GatewaySet{gwKey("team-a", "gw"): ""}
	rs, _ := Inventory(context.Background(), kube, pb.Scope{}, gws, nil)
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/kept"}) {
		t.Errorf("replicas %v, want team-a/kept only", got)
	}
	rs, _ = Inventory(context.Background(), kube, pb.Scope{AllControllers: true}, gws, nil)
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/foreign", "team-a/kept", "team-b/unwatched"}) {
		t.Errorf("all controllers: replicas %v, want every labelled proxy", got)
	}
	// -n bounds proxies even with --all-controllers.
	rs, _ = Inventory(context.Background(), kube, pb.Scope{Namespace: "team-a", AllControllers: true}, gws, nil)
	if got := podNames(rs); !slices.Equal(got, []string{"team-a/foreign", "team-a/kept"}) {
		t.Errorf("-n team-a: replicas %v", got)
	}
}

func TestReidentify(t *testing.T) {
	r := pb.Replica{Namespace: "team-a", Pod: "p", UID: "uid-p"}
	ctx := context.Background()
	same := kubefake.NewClientset(proxyPod("team-a", "p", "gw"))
	if got := Reidentify(ctx, same, r); got != pb.IdentityVerified {
		t.Errorf("same UID: %q", got)
	}
	recreated := proxyPod("team-a", "p", "gw")
	recreated.UID = "uid-new"
	if got := Reidentify(ctx, kubefake.NewClientset(recreated), r); got != pb.IdentityChanged {
		t.Errorf("recreated: %q", got)
	}
	if got := Reidentify(ctx, kubefake.NewClientset(), r); got != pb.IdentityGone {
		t.Errorf("deleted: %q", got)
	}
	denied := kubefake.NewClientset(proxyPod("team-a", "p", "gw"))
	denied.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", nil)
	})
	if got := Reidentify(ctx, denied, r); got != pb.IdentityRereadFailed {
		t.Errorf("403: %q", got)
	}
}
