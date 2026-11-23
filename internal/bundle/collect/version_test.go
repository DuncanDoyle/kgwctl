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
// other Pod's dial until the capture's context ends. busy is closed once
// workers hung dials are held at the same time.
type hangingAdmin struct {
	dials, hung atomic.Int64
	workers     int64
	busy        chan struct{}
}

func (h *hangingAdmin) Dial(ctx context.Context, pod types.NamespacedName, _ int) (io.ReadWriteCloser, func(), error) {
	h.dials.Add(1)
	if !strings.HasPrefix(pod.Name, "good-") {
		if h.hung.Add(1) == h.workers {
			close(h.busy)
		}
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

// versionFixture returns a discovery client serving /version, two good and
// hung replicas, and a fake clientset holding their Pods.
func versionFixture(t *testing.T, hung int) (discovery.DiscoveryInterface, []pb.Replica, *kubefake.Clientset) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"gitVersion":"v1.36.2"}`)
	}))
	t.Cleanup(srv.Close)
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
	for i := range hung {
		add(fmt.Sprintf("hung-%02d", i))
	}
	return disc, replicas, kubefake.NewClientset(pods...)
}

// readVersionFile returns the version.json s holds, checking the server version and entry count.
func readVersionFile(t *testing.T, s *recordSink, replicas int) collect.VersionFile {
	t.Helper()
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
	if len(vf.Observed) != replicas {
		t.Fatalf("observed %d entries, want %d", len(vf.Observed), replicas)
	}
	return vf
}

// TestVersionHungReplicas: once every worker holds a hung capture, the run
// ends (as at its deadline or on Ctrl-C). version.json is still written, with
// the server version, the good replicas ok and every hung one timeout; the
// replicas never started are never dialled and each still gets an error item.
func TestVersionHungReplicas(t *testing.T) {
	const hung, workers = 20, 8
	disc, replicas, kube := versionFixture(t, hung)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &hangingAdmin{workers: workers, busy: make(chan struct{})}
	// All workers held by hung dials means both good captures, queued first, have finished.
	go func() {
		<-d.busy
		cancel()
	}()
	s := newRecordSink()
	env := pb.Env{Kube: kube, Discovery: disc, Replicas: replicas}
	if err := collect.Version(view.BuildInfo{Version: "test"}, nil, d).Collect(ctx, env, s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	vf := readVersionFile(t, s, len(replicas))
	for _, o := range vf.Observed {
		want := pb.ItemTimeout
		if strings.HasPrefix(o.Pod, "good-") {
			want = pb.ItemOK
		}
		if o.Status != want {
			t.Errorf("%s: %s, want %s", o.Pod, o.Status, want)
		}
	}
	if n := d.dials.Load(); n != 2+workers {
		t.Errorf("dialled %d replicas, want %d: none starts after the run ends", n, 2+workers)
	}
	// Every hung replica has its own error item, dialled or not.
	if n := strings.Count(strings.Join(s.errPaths, "\n"), "/server_info.json"); n != hung {
		t.Errorf("%d server_info error items, want %d: %v", n, hung, s.errPaths)
	}
}

// TestVersionCaptureDoesNotFit: when --request-timeout no longer fits before
// the deadline, no capture starts; version.json is written with every
// replica timeout.
func TestVersionCaptureDoesNotFit(t *testing.T) {
	disc, replicas, kube := versionFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	d := &hangingAdmin{busy: make(chan struct{})}
	s := newRecordSink()
	env := pb.Env{Kube: kube, Discovery: disc, Replicas: replicas, RequestTimeout: 2 * time.Hour}
	if err := collect.Version(view.BuildInfo{Version: "test"}, nil, d).Collect(ctx, env, s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, o := range readVersionFile(t, s, len(replicas)).Observed {
		if o.Status != pb.ItemTimeout {
			t.Errorf("%s: %s, want timeout", o.Pod, o.Status)
		}
	}
	if n := d.dials.Load(); n != 0 {
		t.Errorf("dialled %d replicas, want none", n)
	}
}
