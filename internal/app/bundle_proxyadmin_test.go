package app_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

// targetAdmin is a proxydump.Dialer that records each admin request as
// "<pod> <target>" and answers it with serve's 200 body.
type targetAdmin struct {
	mu    sync.Mutex
	reqs  []string
	serve func(pod, target string) string
}

func (f *targetAdmin) Dial(_ context.Context, pod types.NamespacedName, _ int) (io.ReadWriteCloser, func(), error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		target := req.URL.RequestURI()
		f.mu.Lock()
		f.reqs = append(f.reqs, pod.Name+" "+target)
		f.mu.Unlock()
		body := f.serve(pod.Name, target)
		fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}()
	return client, func() { client.Close() }, nil
}

// adminRequests returns the recorded requests other than /server_info, sorted, and resets them.
func (f *targetAdmin) adminRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		if !strings.HasSuffix(r, " /server_info") {
			out = append(out, r)
		}
	}
	f.reqs = nil
	return slices.Sorted(slices.Values(out))
}

// adminBody answers every admin target of pod with a small valid body.
func adminBody(pod, target string) string {
	switch strings.SplitN(target, "?", 2)[0] {
	case "/config_dump":
		return `{"configs":[]}`
	case "/stats":
		return "http.http.rds.listener~80.version_text: \"" + pod + "\"\n"
	case "/clusters":
		return "kube_svc_8080::10.0.0.1:8080::health_flags::healthy\n"
	case "/server_info":
		return serverInfo(pod + "/1.36.1/Clean/RELEASE/BoringSSL")
	}
	return "{}"
}

// TestBundleNoEndpoints: by default each proxy replica gets the EDS config
// dump, stats, certs and clusters; --no-endpoints sends the plain config dump
// and no clusters request. The testenv proxy sees only the port-forward
// upgrade, not the admin request inside the stream, so the fake dialer
// records the requests.
func TestBundleNoEndpoints(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a")}})
	d := &targetAdmin{serve: adminBody}
	skip := []string{"--skip", "check,logs,controller-metrics,proxy-metrics"}

	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Dialer: d}, skip...)
	want := []string{"http-a /certs", "http-a /clusters", "http-a /config_dump?include_eds", "http-a /stats"}
	if got := d.adminRequests(); !slices.Equal(got, want) {
		t.Errorf("default: requests %v, want %v", got, want)
	}
	dir := "proxies/kgateway-system/http-a/"
	assertMembers(t, a, []string{dir + "config_dump.json", dir + "stats.txt", dir + "certs.json", dir + "clusters.txt"}, nil)

	a = archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Dialer: d}, append(skip, "--no-endpoints")...)
	want = []string{"http-a /certs", "http-a /config_dump", "http-a /stats"}
	if got := d.adminRequests(); !slices.Equal(got, want) {
		t.Errorf("--no-endpoints: requests %v, want %v", got, want)
	}
	assertMembers(t, a, []string{dir + "config_dump.json", dir + "stats.txt", dir + "certs.json"}, []string{dir + "clusters.txt"})
}

// TestBundleDivergentReplicas: two replicas at different version_text and
// listener states keep both captures under their own Pod, with the Pod's
// UID in the source, and each extracted config_dump.json loads in
// `proxy routes --file`.
func TestBundleDivergentReplicas(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a", "http-b")}})
	dumps := map[string]string{"http-a": "testdata/proxy/listener-updating.json", "http-b": dumpsDir + "demo.json"}
	d := &targetAdmin{serve: func(pod, target string) string {
		if strings.HasPrefix(target, "/config_dump") {
			b, err := os.ReadFile(dumps[pod])
			if err != nil {
				t.Error(err)
			}
			return string(b)
		}
		return adminBody(pod, target)
	}}
	a := archiveWith(t, env.Kubeconfig, nil, app.BundleTestHooks{Dialer: d}, "--skip", "check,logs,controller-metrics,proxy-metrics")

	kube, err := kubernetes.NewForConfig(env.Admin)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Item struct {
			Items []struct {
				Path, Status string
				Source       struct{ Pod, UID, Operation string }
			} `json:"items"`
		} `json:"item"`
	}
	b, _ := a.File("manifest.json")
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, pod := range []string{"http-a", "http-b"} {
		p, err := kube.CoreV1().Pods("kgateway-system").Get(t.Context(), pod, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		dir := "proxies/kgateway-system/" + pod + "/"
		fileHas(t, a, dir+"stats.txt", `version_text: "`+pod+`"`)
		for _, f := range []string{"config_dump.json", "stats.txt", "certs.json", "clusters.txt"} {
			i := slices.IndexFunc(m.Item.Items, func(it struct {
				Path, Status string
				Source       struct{ Pod, UID, Operation string }
			}) bool {
				return it.Path == dir+f
			})
			if i < 0 {
				t.Errorf("no item %s", dir+f)
				continue
			}
			if it := m.Item.Items[i]; it.Status != "ok" || it.Source.Pod != pod || it.Source.UID != string(p.UID) {
				t.Errorf("%s: %+v, want ok from %s/%s", it.Path, it, pod, p.UID)
			}
		}
	}

	// The extracted dumps load offline, each with its own listener state.
	for pod, want := range map[string]string{"http-a": "active · updating", "http-b": "[orders.team-a.example]"} {
		b, _ := a.File("proxies/kgateway-system/" + pod + "/config_dump.json")
		path := filepath.Join(t.TempDir(), "config_dump.json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		r := runRoutes(t, "unused-kubeconfig", app.ProxyTestHooks{}, "proxy", "routes", "--file", path)
		if r.code != 0 || !strings.Contains(r.stdout, want) {
			t.Errorf("%s: proxy routes --file exit %d, stderr %q; want %q in:\n%s", pod, r.code, r.stderr, want, r.stdout)
		}
		if pod == "http-b" && strings.Contains(r.stdout, "updating") {
			t.Errorf("http-b shows http-a's listener state:\n%s", r.stdout)
		}
	}
	bundletest.AssertNoCanary(t, a, nil)
}
