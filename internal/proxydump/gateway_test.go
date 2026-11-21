package proxydump

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

var testGateway = types.NamespacedName{Namespace: testPod.Namespace, Name: "http"}

// fakeClients serve testGateway with testPod as its one ready replica. Like
// client-go with an exec credential plugin, they ignore ctx.
func fakeClients(getGateway k8stesting.ReactionFunc) load.Clients {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("get", "gateways", getGateway)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: testPod.Namespace, Name: testPod.Name, Labels: map[string]string{gatewayNameLabel: testGateway.Name}},
		Status:     corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	return load.Clients{Dynamic: dyn, Kube: kubefake.NewClientset(pod)}
}

func gatewayFound(k8stesting.Action) (bool, runtime.Object, error) {
	gw := &unstructured.Unstructured{}
	gw.SetAPIVersion("gateway.networking.k8s.io/v1")
	gw.SetKind("Gateway")
	gw.SetNamespace(testGateway.Namespace)
	gw.SetName(testGateway.Name)
	return true, gw, nil
}

// requireDeadline runs FetchGateway with a short deadline and checks it
// returns ctx's error by then.
func requireDeadline(t *testing.T, c load.Clients, d Dialer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	capture, err := FetchGateway(ctx, c, d, testGateway, "", time.Now)
	if !errors.Is(err, context.DeadlineExceeded) || capture != nil {
		t.Fatalf("FetchGateway = %v, %v; want nil and DeadlineExceeded", capture, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("returned after %v, want by the 200ms deadline", took)
	}
}

// TestFetchGatewaySelectBlocks: a Gateway lookup that ignores ctx does not
// hold FetchGateway past its deadline, and its late success dials nothing.
func TestFetchGatewaySelectBlocks(t *testing.T) {
	held := make(chan struct{})
	selected := make(chan struct{})
	c := fakeClients(func(a k8stesting.Action) (bool, runtime.Object, error) {
		hold(held)
		defer close(selected)
		return gatewayFound(a)
	})
	requireDeadline(t, c, dialerFunc(func(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error) {
		t.Error("dialed after the deadline")
		return nil, nil, errors.New("unexpected dial")
	}))
	close(held)
	select {
	case <-selected:
	case <-time.After(waitBound):
		t.Fatal("the held Gateway lookup never returned")
	}
}

// TestFetchGatewayDialBlocks: a dial that ignores ctx does not hold
// FetchGateway past its deadline; the tunnel it opens late is released unused.
func TestFetchGatewayDialBlocks(t *testing.T) {
	ft := newFakeTunnel(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })
	held := make(chan struct{})
	requireDeadline(t, fakeClients(gatewayFound), heldDialer(ft, held))
	close(held)
	ft.requireConnClosed(t)
	ft.waitListenerClosed(t)
	if n := len(ft.requests()); n != 0 {
		t.Fatalf("late tunnel served %d requests, want 0", n)
	}
}

// TestFetchGatewayFetchError: a failed fetch names the Pod it selected; a
// failed selection does not.
func TestFetchGatewayFetchError(t *testing.T) {
	dialErr := errors.New("dial failed")
	d := dialerFunc(func(context.Context, types.NamespacedName, int) (io.ReadWriteCloser, func(), error) {
		return nil, nil, dialErr
	})
	_, err := FetchGateway(t.Context(), fakeClients(gatewayFound), d, testGateway, "", time.Now)
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Pod != testPod || !errors.Is(err, dialErr) {
		t.Fatalf("err = %v, want a FetchError for %s wrapping %v", err, testPod, dialErr)
	}
	_, err = FetchGateway(t.Context(), fakeClients(gatewayFound), d, testGateway, "other", time.Now)
	if !errors.Is(err, ErrNotReplica) || errors.As(err, &fe) {
		t.Fatalf("err = %v, want ErrNotReplica and no FetchError", err)
	}
}
