package bundle

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// Proxy Pod identification (design §8.2).
const (
	// ProxyLabel and ProxyLabelValue mark a kgateway proxy Pod.
	ProxyLabel      = "kgateway"
	ProxyLabelValue = "kube-gateway"
	// GatewayFullNameAnnotation names a proxy Pod's Gateway in its namespace.
	GatewayFullNameAnnotation = "gateway.kgateway.dev/gateway-full-name"
	// ProxyMetricsPort is the proxy's metrics port, tried whether declared or not.
	ProxyMetricsPort = 9091
)

// gatewayNameLabel is the Gateway API name label; shortened past 63 characters.
const gatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

// GatewaySet maps each kept Gateway to its owning install ID, "" when
// ambiguous or unowned. The inventory captures the proxies of every key.
type GatewaySet map[types.NamespacedName]string

// ControllerSelector selects an install's controller Pods: its Deployment's spec.selector.
type ControllerSelector struct {
	Install  pb.Install
	Selector labels.Selector
}

// ReplicaError is an inventory failure for the item at Path.
type ReplicaError struct {
	Path   pb.Path
	Source pb.Source
	Err    error
}

func (e *ReplicaError) Error() string { return e.Err.Error() }
func (e *ReplicaError) Unwrap() error { return e.Err }

// errUnverified is the error line of a proxy Pod without the full-name annotation.
var errUnverified = errors.New("proxy Pod has no " + GatewayFullNameAnnotation + " annotation: identity unverified, not captured")

// Inventory lists the run's controller and proxy replicas once (design
// §8.2): controller Pods by each install's selector, then proxy Pods in s's
// namespace bound whose annotation names a GatewaySet Gateway, or any
// Gateway with s.AllControllers; only a GatewaySet Gateway is recorded as its
// owner. A proxy Pod without the annotation is kept as unverified with an
// error, and the owner chain is never read.
func Inventory(ctx context.Context, kube kubernetes.Interface, s pb.Scope, gws GatewaySet, ctrls []ControllerSelector) ([]pb.Replica, []error) {
	var (
		out  []pb.Replica
		errs []error
	)
	for _, c := range ctrls {
		in := c.Install
		pods, err := kube.CoreV1().Pods(in.Namespace).List(ctx, metav1.ListOptions{LabelSelector: c.Selector.String()})
		if err != nil {
			errs = append(errs, &ReplicaError{Path: pb.Path{pb.ControllerDir, in.Namespace}, Source: pb.Source{Namespace: in.Namespace, Operation: pb.OpList}, Err: err})
			continue
		}
		var rs []pb.Replica
		for i := range pods.Items {
			r := replica(&pods.Items[i], pb.RoleController, in.Namespace+"/"+in.Deployment, pb.IdentitySelector, in.MetricsPort)
			rs = append(rs, r)
		}
		slices.SortFunc(rs, func(a, b pb.Replica) int { return cmp.Compare(a.Pod, b.Pod) })
		out = append(out, rs...)
	}

	sel := labels.SelectorFromSet(labels.Set{ProxyLabel: ProxyLabelValue}).String()
	pods, err := kube.CoreV1().Pods(s.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		p := pb.Path{pb.ProxiesDir}
		if s.Namespace != "" {
			p = append(p, s.Namespace)
		}
		return out, append(errs, &ReplicaError{Path: p, Source: pb.Source{Namespace: s.Namespace, Operation: pb.OpList}, Err: err})
	}
	var proxies []pb.Replica
	for i := range pods.Items {
		p := &pods.Items[i]
		gw, annotated := p.Annotations[GatewayFullNameAnnotation]
		if !annotated {
			// The label only decides whether to report the Pod; within -n every one is reported.
			if s.Namespace == "" && !s.AllControllers && !labelMatches(gws, p.Namespace, p.Labels[gatewayNameLabel]) {
				continue
			}
			proxies = append(proxies, replica(p, pb.RoleProxy, "", pb.IdentityUnverified, ProxyMetricsPort))
			errs = append(errs, &ReplicaError{Path: pb.Path{pb.ProxiesDir, p.Namespace, p.Name},
				Source: pb.Source{Namespace: p.Namespace, Pod: p.Name, UID: p.UID}, Err: errUnverified})
			continue
		}
		_, known := gws[types.NamespacedName{Namespace: p.Namespace, Name: gw}]
		if !known && !s.AllControllers {
			continue
		}
		// Only a valid name of a known Gateway becomes the owner; other annotation values stay out of the manifest.
		owner := ""
		if known && len(validation.IsDNS1123Subdomain(gw)) == 0 {
			owner = p.Namespace + "/" + gw
		}
		proxies = append(proxies, replica(p, pb.RoleProxy, owner, pb.IdentityAnnotation, ProxyMetricsPort))
	}
	slices.SortFunc(proxies, func(a, b pb.Replica) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Pod, b.Pod))
	})
	return append(out, proxies...), errs
}

// labelMatches reports whether label is the gateway-name label of a GatewaySet Gateway in ns.
func labelMatches(gws GatewaySet, ns, label string) bool {
	if label == "" {
		return false
	}
	for k := range gws {
		if k.Namespace == ns && safeLabelValue(k.Name) == label {
			return true
		}
	}
	return false
}

// safeLabelValue mirrors kgateway's kgateway.gateway.safeLabelValue Helm helper.
func safeLabelValue(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return strings.TrimSuffix(name[:50], "-") + "-" + hex.EncodeToString(sum[:])[:12]
}

// replica records p's state; restart counts include init containers.
func replica(p *corev1.Pod, role pb.Role, owner string, id pb.Identity, port int32) pb.Replica {
	r := pb.Replica{
		Namespace: p.Namespace, Pod: p.Name, UID: p.UID, Role: role, Owner: owner, Identity: id,
		Terminating: p.DeletionTimestamp != nil, Phase: p.Status.Phase, MetricsPort: port,
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			r.Ready = c.Status == corev1.ConditionTrue
		}
	}
	if p.Status.StartTime != nil {
		r.StartTime = p.Status.StartTime.Time
	}
	restarts := func(sts []corev1.ContainerStatus, name string) int32 {
		for _, st := range sts {
			if st.Name == name {
				return st.RestartCount
			}
		}
		return 0
	}
	for _, c := range p.Spec.InitContainers {
		r.Containers = append(r.Containers, pb.Container{Name: c.Name, Init: true, Restarts: restarts(p.Status.InitContainerStatuses, c.Name)})
	}
	for _, c := range p.Spec.Containers {
		r.Containers = append(r.Containers, pb.Container{Name: c.Name, Restarts: restarts(p.Status.ContainerStatuses, c.Name)})
		for _, cp := range c.Ports {
			if port != 0 && cp.ContainerPort == port {
				r.MetricsPortDeclared = true
			}
		}
	}
	return r
}

// Reidentify reads r's Pod again after a capture (design §8.2): verified
// for the same UID, changed for another Pod of that name, gone when deleted,
// reread-failed on any other error.
func Reidentify(ctx context.Context, kube kubernetes.Interface, r pb.Replica) pb.CaptureIdentity {
	p, err := kube.CoreV1().Pods(r.Namespace).Get(ctx, r.Pod, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return pb.IdentityGone
	case err != nil:
		return pb.IdentityRereadFailed
	case p.UID != r.UID:
		return pb.IdentityChanged
	}
	return pb.IdentityVerified
}
