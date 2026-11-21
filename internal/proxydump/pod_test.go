package proxydump_test

import (
	"context"
	stderrors "errors"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

// podsEnv starts an Env with the replica fixture and returns its clients.
func podsEnv(t *testing.T) load.Clients {
	t.Helper()
	env := testenv.New(t, testenv.Options{Fixtures: []string{"testdata/pods.yaml"}})
	cfg, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	c, err := load.NewClients(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// web-c holds a finalizer so it can be made terminating; drop it so cleanup can delete the Pod.
	t.Cleanup(func() {
		admin, err := kubernetes.NewForConfig(env.Admin) // c is bound to the test context, which is done by now
		if err != nil {
			t.Error(err)
			return
		}
		patch := []byte(`{"metadata":{"finalizers":null}}`)
		if _, err := admin.CoreV1().Pods("team-a").Patch(context.Background(), "web-c", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !errors.IsNotFound(err) {
			t.Error(err)
		}
	})
	return c
}

func nn(ns, name string) types.NamespacedName { return types.NamespacedName{Namespace: ns, Name: name} }

// terminate sets deletionTimestamp on name; the fixture's finalizer keeps the Pod.
func terminate(t *testing.T, c load.Clients, name string) {
	t.Helper()
	if err := c.Kube.CoreV1().Pods("team-a").Delete(t.Context(), name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func deleteAll(t *testing.T, c load.Clients, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := c.Kube.CoreV1().Pods("team-a").Delete(t.Context(), n, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectPodPicksOldestReady(t *testing.T) {
	c := podsEnv(t)
	terminate(t, c, "web-c")
	got, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "web"), "")
	if err != nil || got != nn("team-a", "web-b") {
		t.Fatalf("got %v, %v; want team-a/web-b", got, err)
	}
}

func TestSelectPodStableOnTie(t *testing.T) {
	c := podsEnv(t)
	for i := range 5 {
		got, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "tie"), "")
		if err != nil || got != nn("team-a", "tie-a") {
			t.Fatalf("call %d: got %v, %v; want team-a/tie-a", i, got, err)
		}
	}
}

func TestSelectPodSkipsTerminating(t *testing.T) {
	c := podsEnv(t)
	terminate(t, c, "web-c")
	deleteAll(t, c, "web-a", "web-b")
	_, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "web"), "")
	if !stderrors.Is(err, proxydump.ErrNoReplica) {
		t.Fatalf("err = %v, want ErrNoReplica", err)
	}
	for _, want := range []string{"web-c (terminating)", "web-d (not ready)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q lacks %q", err, want)
		}
	}
}

func TestSelectPodExplicitUnready(t *testing.T) {
	c := podsEnv(t)
	got, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "web"), "web-d")
	if err != nil || got != nn("team-a", "web-d") {
		t.Fatalf("got %v, %v; want team-a/web-d", got, err)
	}
}

func TestSelectPodRejectsOtherGateway(t *testing.T) {
	c := podsEnv(t)
	_, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "web"), "other-x")
	if !stderrors.Is(err, proxydump.ErrNotReplica) {
		t.Fatalf("err = %v, want ErrNotReplica", err)
	}
}

func TestSelectPodGatewayNotFound(t *testing.T) {
	c := podsEnv(t)
	_, err := proxydump.SelectPod(t.Context(), c, nn("team-a", "nope"), "")
	if err == nil || !strings.Contains(err.Error(), "Gateway team-a/nope not found") {
		t.Fatalf("err = %v, want it to say Gateway team-a/nope not found", err)
	}
}
