// Package proxydump reads what an Envoy replica of a Gateway holds (design
// phase 3 §4).
package proxydump

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

// gatewayNameLabel is set on every Pod a Gateway's proxy Deployment creates.
const gatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

var gatewaysGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}

var (
	// ErrNoReplica means the Gateway has no Pod that is ready and not terminating.
	ErrNoReplica = errors.New("no ready replica")
	// ErrNotReplica means the named Pod does not belong to the Gateway.
	ErrNotReplica = errors.New("pod is not a replica of the Gateway")
)

// SelectPod returns the Envoy replica of gw to dump. An empty pod picks the
// oldest ready, non-terminating replica (ties by name). A named pod is only
// checked to be a replica of gw, whatever its state.
func SelectPod(ctx context.Context, c load.Clients, gw types.NamespacedName, pod string) (types.NamespacedName, error) {
	if _, err := c.Dynamic.Resource(gatewaysGVR).Namespace(gw.Namespace).Get(ctx, gw.Name, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return types.NamespacedName{}, fmt.Errorf("Gateway %s not found", gw)
		}
		return types.NamespacedName{}, fmt.Errorf("get Gateway %s: %w", gw, err)
	}
	list, err := c.Kube.CoreV1().Pods(gw.Namespace).List(ctx, metav1.ListOptions{LabelSelector: gatewayNameLabel + "=" + gw.Name})
	if err != nil {
		return types.NamespacedName{}, fmt.Errorf("list Pods of Gateway %s: %w", gw, err)
	}
	pods := list.Items

	if pod != "" {
		if slices.ContainsFunc(pods, func(p corev1.Pod) bool { return p.Name == pod }) {
			return types.NamespacedName{Namespace: gw.Namespace, Name: pod}, nil
		}
		return types.NamespacedName{}, fmt.Errorf("%w %s: Pod %s/%s does not carry %s=%s", ErrNotReplica, gw, gw.Namespace, pod, gatewayNameLabel, gw.Name)
	}

	var ready []corev1.Pod
	var skipped []string
	for _, p := range pods {
		switch {
		case p.DeletionTimestamp != nil:
			skipped = append(skipped, p.Name+" (terminating)")
		case !isReady(p):
			skipped = append(skipped, p.Name+" (not ready)")
		default:
			ready = append(ready, p)
		}
	}
	if len(ready) == 0 {
		detail := "it has no Pods"
		if len(skipped) > 0 {
			slices.Sort(skipped)
			detail = strings.Join(skipped, ", ")
		}
		return types.NamespacedName{}, fmt.Errorf("%w for Gateway %s: %s", ErrNoReplica, gw, detail)
	}
	oldest := slices.MinFunc(ready, func(a, b corev1.Pod) int {
		if c := startTime(a).Compare(startTime(b)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return types.NamespacedName{Namespace: gw.Namespace, Name: oldest.Name}, nil
}

func isReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// startTime is the zero time for a Pod that has not started, so it sorts first.
func startTime(p corev1.Pod) time.Time {
	if p.Status.StartTime == nil {
		return time.Time{}
	}
	return p.Status.StartTime.Time
}
