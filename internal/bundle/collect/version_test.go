package collect_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	"github.com/DuncanDoyle/kgwctl/internal/view"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// hangingAdmin serves /server_info for pods named good-*, and holds every
// other Pod's dial until the capture's context ends.
type hangingAdmin struct{ dials atomic.Int64 }

func (h *hangingAdmin) Dial(ctx context.Context, pod types.NamespacedName, _ int) (io.ReadWriteCloser, func(), error) {
	h.dials.Add(1)
	if !strings.HasPrefix(pod.Name, "good-") {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
			return
		}
		body := `{"version":"v/1.36.1/Clean/RELEASE/BoringSSL"}`
		fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}()
	return client, func() { client.Close() }, nil
}

// TestVersionHungReplicas: with replicas that hang, version.json is still
// written before the deadline, with the server version, the good replicas ok
// and the hung ones timeout; replicas that no longer fit are never dialled.
func TestVersionHungReplicas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"gitVersion":"v1.36.2"}`)
	}))
	defer srv.Close()
	disc, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	var (
		replicas []pb.Replica
		pods     []runtime.Object
	)
	add := func(name string) {
		uid := types.UID("uid-" + name)
		replicas = append(replicas, pb.Replica{Namespace: "gw", Pod: name, UID: uid, Role: pb.RoleProxy,
			Identity: pb.IdentityAnnotation, Phase: corev1.PodRunning})
		pods = append(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: name, UID: uid}})
	}
	add("good-a")
	add("good-b")
	const hung = 20
	for i := range hung {
		add(fmt.Sprintf("hung-%02d", i))
	}
	kube := kubefake.NewClientset(pods...)

	// Request bound 150ms, deadline 400ms: two waves of hung captures fit, the rest do not.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	d := &hangingAdmin{}
	s := newRecordSink()
	env := pb.Env{Kube: kube, Discovery: disc, Replicas: replicas, RequestTimeout: 150 * time.Millisecond}
	if err := collect.Version(view.BuildInfo{Version: "test"}, nil, d).Collect(ctx, env, s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if ctx.Err() != nil {
		t.Error("Collect returned after the deadline")
	}
	b, ok := s.json["cluster/version.json"]
	if !ok {
		t.Fatal("version.json not written")
	}
	var vf collect.VersionFile
	if err := json.Unmarshal(b, &vf); err != nil {
		t.Fatal(err)
	}
	if vf.Server == nil || vf.Server.GitVersion != "v1.36.2" {
		t.Errorf("server %+v", vf.Server)
	}
	if len(vf.Observed) != len(replicas) {
		t.Fatalf("observed %d entries, want %d", len(vf.Observed), len(replicas))
	}
	for _, o := range vf.Observed {
		want := pb.ItemTimeout
		if strings.HasPrefix(o.Pod, "good-") {
			want = pb.ItemOK
		}
		if o.Status != want {
			t.Errorf("%s: %s, want %s", o.Pod, o.Status, want)
		}
	}
	if n := d.dials.Load(); n >= int64(len(replicas)) {
		t.Errorf("dialled %d of %d replicas; captures that cannot fit must not start", n, len(replicas))
	}
	// Every hung replica has its own error item, dialled or not.
	if n := strings.Count(strings.Join(s.errPaths, "\n"), "/server_info.json"); n != hung {
		t.Errorf("%d server_info error items, want %d: %v", n, hung, s.errPaths)
	}
}
