package load_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

func TestBindContextCancelsInFlight(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: "pods"}, testenv.Action{Delay: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := load.NewClients(ctx, restConfig(t, env))
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err = c.Kube.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d >= 500*time.Millisecond {
		t.Fatalf("request returned after %v, want < 500ms", d)
	}
	if hits.N() == 0 {
		t.Fatal("delay rule never matched")
	}
}

func TestBindContextAfterDone(t *testing.T) {
	env := testenv.New(t, testenv.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err := load.NewClients(ctx, restConfig(t, env))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Kube.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := env.Proxies[0].Requests(); len(got) != 0 {
		t.Fatalf("proxy saw %v, want no requests", got)
	}
}

func restConfig(t *testing.T, env *testenv.Env) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
