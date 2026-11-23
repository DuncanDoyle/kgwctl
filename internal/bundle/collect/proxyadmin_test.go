package collect_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// okResponse is a 200 admin response with body.
func okResponse(body string) string {
	return fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
}

// adminReply is what a fake admin port sends: raw bytes, then err in place
// of EOF (the kubelet's error stream), or dialErr before any stream.
type adminReply struct {
	raw     string
	err     error
	dialErr error
	// hang holds the dial until its context ends.
	hang bool
}

// fakeAdmin is a proxydump.Dialer that records each request as
// "<pod> <target>" and answers it with reply.
type fakeAdmin struct {
	mu    sync.Mutex
	reqs  []string
	reply func(pod, target string) adminReply
}

func (f *fakeAdmin) Dial(ctx context.Context, pod types.NamespacedName, port int) (io.ReadWriteCloser, func(), error) {
	if port != proxydump.AdminPort {
		return nil, nil, fmt.Errorf("port %d", port)
	}
	if r := f.reply(pod.Name, ""); r.hang {
		f.record(pod.Name + " dial")
		<-ctx.Done()
		return nil, nil, ctx.Err()
	} else if r.dialErr != nil {
		f.record(pod.Name + " dial")
		return nil, nil, r.dialErr
	}
	return &adminConn{f: f, pod: pod.Name}, func() {}, nil
}

func (f *fakeAdmin) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, s)
}

// requests returns the recorded requests, sorted.
func (f *fakeAdmin) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(slices.Values(f.reqs))
}

// adminConn takes one request and replies once the first read starts.
type adminConn struct {
	f    *fakeAdmin
	pod  string
	req  bytes.Buffer
	once sync.Once
	r    io.Reader
}

func (c *adminConn) Write(p []byte) (int, error) { return c.req.Write(p) }
func (c *adminConn) Close() error                { return nil }

func (c *adminConn) Read(p []byte) (int, error) {
	c.once.Do(func() {
		line, _, _ := strings.Cut(c.req.String(), "\r\n")
		f := strings.Fields(line) // GET <target> HTTP/1.1
		target := ""
		if len(f) == 3 && f[0] == "GET" {
			target = f[1]
		}
		c.f.record(c.pod + " " + target)
		rep := c.f.reply(c.pod, target)
		tail := rep.err
		if tail == nil {
			tail = io.EOF
		}
		c.r = io.MultiReader(strings.NewReader(rep.raw), errReader{tail})
	})
	return c.r.Read(p)
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// adminBodies answers every admin target with a small valid body.
func adminBodies(pod, target string) adminReply {
	switch strings.SplitN(target, "?", 2)[0] {
	case "/config_dump":
		return adminReply{raw: okResponse(`{"configs":[{"@type":"type.googleapis.com/envoy.admin.v3.BootstrapConfigDump","bootstrap":{"node":{"id":"` + pod + `"}}}]}`)}
	case "/stats":
		return adminReply{raw: okResponse("server.live: 1\nhttp.http.rds.listener~80.version_text: \"" + pod + "-v1\"\n")}
	case "/certs":
		return adminReply{raw: okResponse(`{"certificates":[{"ca_cert":[{"subject_alt_names":[{"dns":"` + pod + `.example.com"}]}]}]}`)}
	case "/clusters":
		return adminReply{raw: okResponse("kube_gw_svc_8080::10.0.0.1:8080::health_flags::healthy\n")}
	}
	return adminReply{raw: "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"}
}

// runProxyAdmin runs ProxyAdmin through the real writer.
func runProxyAdmin(t *testing.T, env pb.Env, d proxydump.Dialer, noEndpoints bool) (ib.Manifest, bundletest.Archive) {
	t.Helper()
	r := newLogsRun(t)
	m, err := ib.Run(context.Background(), []pb.Collector{collect.ProxyAdmin(d, noEndpoints)}, env, r.w, ib.Manifest{}, ib.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return m, bundletest.Extract(t, &r.buf)
}

// adminFiles are a replica's admin members and their operations, by default.
var adminFiles = map[string]pb.Op{
	pb.ConfigDumpFile: pb.OpConfigDumpEDS, pb.StatsFile: pb.OpStats, pb.CertsFile: pb.OpCerts, pb.ClustersFile: pb.OpClusters,
}

// TestProxyAdminPerReplica: two replicas get all four captures, ok and
// verified; a failing dial gives four failed items, one dial and no member; a replica
// that is not running gets four skipped items and no request; an unverified
// one gets nothing. The default sends the EDS config dump and clusters.
func TestProxyAdminPerReplica(t *testing.T) {
	unverified := proxyReplica("unverified", corev1.PodRunning, true)
	unverified.Identity = pb.IdentityUnverified
	rs := []pb.Replica{
		proxyReplica("a", corev1.PodRunning, true),
		proxyReplica("b", corev1.PodRunning, true),
		proxyReplica("bad", corev1.PodRunning, true),
		proxyReplica("pending", corev1.PodPending, true),
		unverified,
	}
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		if pod == "bad" {
			return adminReply{dialErr: errors.New("dial failed")}
		}
		return adminBodies(pod, target)
	}}
	m, a := runProxyAdmin(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, d, false)

	for _, pod := range []string{"a", "b"} {
		for f, op := range adminFiles {
			p := "proxies/gw/" + pod + "/" + f
			it := manifestItem(t, m, p)
			if it.Status != pb.ItemOK || it.Source.Identity != pb.IdentityVerified || it.Source.Operation != op || it.Source.UID != podUID(pod) {
				t.Errorf("%s: item %+v, want ok, verified, %s", p, it, op)
			}
			if _, ok := a.File(p); !ok {
				t.Errorf("archive lacks %s", p)
			}
		}
	}
	for f := range adminFiles {
		for pod, want := range map[string]pb.ItemStatus{"bad": pb.ItemFailed, "pending": pb.ItemSkipped} {
			p := "proxies/gw/" + pod + "/" + f
			if it := manifestItem(t, m, p); it.Status != want {
				t.Errorf("%s: item %+v, want %s", p, it, want)
			}
			if _, ok := a.File(p); ok {
				t.Errorf("archive has %s", p)
			}
		}
	}
	fileHas := func(p, sub string) {
		if b, _ := a.File(p); !strings.Contains(string(b), sub) {
			t.Errorf("%s lacks %q:\n%s", p, sub, b)
		}
	}
	fileHas("proxies/gw/a/stats.txt", `version_text: "a-v1"`)
	fileHas("proxies/gw/b/certs.json", "b.example.com")
	fileHas("proxies/gw/a/clusters.txt", "health_flags::healthy")
	for _, it := range m.Items {
		if strings.Contains(it.Path, "unverified") || strings.HasSuffix(it.Path, pb.ServerInfoFile) {
			t.Errorf("unexpected item %+v", it)
		}
	}
	want := []string{
		"a /certs", "a /clusters", "a /config_dump?include_eds", "a /stats",
		"b /certs", "b /clusters", "b /config_dump?include_eds", "b /stats",
		"bad dial",
	}
	if got := d.requests(); !slices.Equal(got, want) {
		t.Errorf("requests %v, want %v", got, want)
	}
	if c := m.Collectors; len(c) != 1 || c[0].Name != "proxy-admin" {
		t.Errorf("collectors %+v", c)
	}
}

// TestProxyAdminNoEndpoints: with noEndpoints the replica gets the plain
// config dump, stats and certs, and no clusters item.
func TestProxyAdminNoEndpoints(t *testing.T) {
	rs := []pb.Replica{proxyReplica("a", corev1.PodRunning, true)}
	d := &fakeAdmin{reply: adminBodies}
	m, a := runProxyAdmin(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, d, true)
	if got, want := d.requests(), []string{"a /certs", "a /config_dump", "a /stats"}; !slices.Equal(got, want) {
		t.Errorf("requests %v, want %v", got, want)
	}
	if it := manifestItem(t, m, "proxies/gw/a/config_dump.json"); it.Source.Operation != pb.OpConfigDump || it.Status != pb.ItemOK {
		t.Errorf("config dump item %+v, want ok config-dump", it)
	}
	for _, it := range m.Items {
		if strings.HasSuffix(it.Path, pb.ClustersFile) {
			t.Errorf("clusters item %+v", it)
		}
	}
	if _, ok := a.File("proxies/gw/a/clusters.txt"); ok {
		t.Error("archive has clusters.txt")
	}
}

// TestProxyAdminIdentity: a Pod replaced during its capture gives changed
// items, a Pod deleted during it gives gone items, both ok; an unverified
// replica gets no request.
func TestProxyAdminIdentity(t *testing.T) {
	unverified := proxyReplica("unverified", corev1.PodRunning, true)
	unverified.Identity = pb.IdentityUnverified
	rs := []pb.Replica{proxyReplica("replaced", corev1.PodRunning, true), proxyReplica("deleted", corev1.PodRunning, true), unverified}
	kube := podsOf(rs...)
	var once sync.Map
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		// The first request changes the Pod, before any item is written.
		if _, done := once.LoadOrStore(pod, true); !done {
			pods := kube.CoreV1().Pods("gw")
			_ = pods.Delete(context.Background(), pod, metav1.DeleteOptions{})
			if pod == "replaced" {
				p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: pod, UID: podUID("replaced-2")}}
				if _, err := pods.Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
					t.Error(err)
				}
			}
		}
		return adminBodies(pod, target)
	}}
	m, _ := runProxyAdmin(t, pb.Env{Kube: kube, Replicas: rs}, d, false)
	for pod, id := range map[string]pb.CaptureIdentity{"replaced": pb.IdentityChanged, "deleted": pb.IdentityGone} {
		for f := range adminFiles {
			p := "proxies/gw/" + pod + "/" + f
			if it := manifestItem(t, m, p); it.Status != pb.ItemOK || it.Source.Identity != id {
				t.Errorf("%s: item %+v, want ok and %s", p, it, id)
			}
		}
	}
	for _, r := range d.requests() {
		if strings.HasPrefix(r, "unverified ") {
			t.Errorf("unverified replica got %q", r)
		}
	}
}

// TestProxyAdminArchive: the corpus config dumps of design §10 rows 9-13,
// captured as replicas' config dumps, leave no canary in the archive and
// keep what each case keeps, Envoy's own [redacted] included.
func TestProxyAdminArchive(t *testing.T) {
	dumps := map[string]bundletest.Fixture{}
	var canaries []string
	for _, fx := range bundletest.Corpus(t) {
		if fx.Method == bundletest.MethodConfigDump && fx.Row >= 9 && fx.Row <= 13 {
			dumps[fmt.Sprintf("row-%02d", fx.Row)] = fx
			canaries = append(canaries, fx.Canaries...)
		}
	}
	if len(dumps) != 5 {
		t.Fatalf("found %d config dump cases in rows 9-13, want 5", len(dumps))
	}
	var rs []pb.Replica
	for pod := range dumps {
		rs = append(rs, proxyReplica(pod, corev1.PodRunning, true))
	}
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		if strings.HasPrefix(target, "/config_dump") {
			return adminReply{raw: okResponse(string(dumps[pod].Input))}
		}
		return adminBodies(pod, target)
	}}
	m, a := runProxyAdmin(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, d, false)
	for pod, fx := range dumps {
		p := "proxies/gw/" + pod + "/" + pb.ConfigDumpFile
		if it := manifestItem(t, m, p); it.Status != pb.ItemOK {
			t.Errorf("%s: item %+v", p, it)
		}
		b, _ := a.File(p)
		for _, k := range fx.Keep {
			if !strings.Contains(string(b), k) {
				t.Errorf("%s lost %q", p, k)
			}
		}
	}
	bundletest.AssertNoCanary(t, a, canaries)
}

// TestProxyAdminKubeletCanary: kubelet error-stream text, before the
// response or in the middle of a body, and a malformed status line reach no
// member. A kubelet error fails the tunnel, so the replica gets no further
// request and its remaining items fail; a text item it cuts stays ok with a
// read-error marker. A malformed status line fails only its own item.
func TestProxyAdminKubeletCanary(t *testing.T) {
	const canary = "kgwctl-canary-kubelet-admin"
	kubelet := errors.New("port-forward to port 19000 failed: " + canary)
	// Headers and part of the body, then the error stream ends the read.
	cut := adminReply{raw: "HTTP/1.1 200 OK\r\nContent-Length: 4096\r\n\r\n{\"configs\":[\nline one\n", err: kubelet}
	rs := []pb.Replica{
		proxyReplica("early", corev1.PodRunning, true),
		proxyReplica("midjson", corev1.PodRunning, true),
		proxyReplica("midtext", corev1.PodRunning, true),
		proxyReplica("malformed", corev1.PodRunning, true),
	}
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		switch {
		case pod == "early":
			return adminReply{err: kubelet}
		case pod == "midjson", pod == "midtext" && target == "/stats":
			return cut
		case pod == "malformed":
			// Go quotes the bad status code in its error.
			return adminReply{raw: "HTTP/1.1 " + canary + " OK\r\nContent-Length: 0\r\n\r\n"}
		}
		return adminBodies(pod, target)
	}}
	m, a := runProxyAdmin(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, d, false)
	for _, pod := range []string{"early", "midjson", "malformed"} {
		for f := range adminFiles {
			if it := manifestItem(t, m, "proxies/gw/"+pod+"/"+f); it.Status != pb.ItemFailed {
				t.Errorf("%s/%s: item %+v, want failed", pod, f, it)
			}
		}
	}
	if it := manifestItem(t, m, "proxies/gw/midtext/config_dump.json"); it.Status != pb.ItemOK {
		t.Errorf("midtext config dump %+v, want ok", it)
	}
	stats := manifestItem(t, m, "proxies/gw/midtext/stats.txt")
	if stats.Status != pb.ItemOK || stats.Reason != "truncated: read error" {
		t.Errorf("midtext stats %+v, want ok and truncated: read error", stats)
	}
	if b, _ := a.File("proxies/gw/midtext/stats.txt"); string(b) != "{\"configs\":[\nline one\n[kgwctl: truncated: read error]\n" {
		t.Errorf("midtext stats.txt = %q", b)
	}
	for _, f := range []string{pb.CertsFile, pb.ClustersFile} {
		if it := manifestItem(t, m, "proxies/gw/midtext/"+f); it.Status != pb.ItemFailed {
			t.Errorf("midtext/%s: item %+v, want failed", f, it)
		}
	}
	perPod := map[string]int{}
	for _, r := range d.requests() {
		pod, _, _ := strings.Cut(r, " ")
		perPod[pod]++
	}
	if want := map[string]int{"early": 1, "midjson": 1, "midtext": 2, "malformed": 4}; !maps.Equal(perPod, want) {
		t.Errorf("requests per pod %v, want %v", perPod, want)
	}
	bundletest.AssertNoCanary(t, a, []string{canary})
}

// TestProxyAdminHungReplica: a replica whose dial hangs gets one request;
// once it times out, its other items are recorded as timeout without another
// dial (design §3: a hung pod costs one item). Healthy replicas are captured.
func TestProxyAdminHungReplica(t *testing.T) {
	rs := []pb.Replica{proxyReplica("hung", corev1.PodRunning, true), proxyReplica("a", corev1.PodRunning, true)}
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		if pod == "hung" {
			return adminReply{hang: true}
		}
		return adminBodies(pod, target)
	}}
	// The fake answers healthy requests in memory, well within the timeout.
	env := pb.Env{Kube: podsOf(rs...), Replicas: rs, RequestTimeout: time.Second}
	m, _ := runProxyAdmin(t, env, d, false)
	var hung []string
	for _, r := range d.requests() {
		if strings.HasPrefix(r, "hung ") {
			hung = append(hung, r)
		}
	}
	if !slices.Equal(hung, []string{"hung dial"}) {
		t.Errorf("hung replica requests %v, want one dial", hung)
	}
	for f := range adminFiles {
		if it := manifestItem(t, m, "proxies/gw/hung/"+f); it.Status != pb.ItemTimeout {
			t.Errorf("hung/%s: item %+v, want timeout", f, it)
		}
		if it := manifestItem(t, m, "proxies/gw/a/"+f); it.Status != pb.ItemOK {
			t.Errorf("a/%s: item %+v, want ok", f, it)
		}
	}
}

// TestProxyAdminTimeoutMidText: a stats body cut by the request timeout stays
// ok with a timeout marker, and the replica gets no further request.
func TestProxyAdminTimeoutMidText(t *testing.T) {
	rs := []pb.Replica{proxyReplica("slow", corev1.PodRunning, true)}
	d := &fakeAdmin{reply: func(pod, target string) adminReply {
		if target == "/stats" {
			return adminReply{raw: "HTTP/1.1 200 OK\r\nContent-Length: 4096\r\n\r\nserver.live: 1\n", err: context.DeadlineExceeded}
		}
		return adminBodies(pod, target)
	}}
	m, a := runProxyAdmin(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, d, false)
	if it := manifestItem(t, m, "proxies/gw/slow/stats.txt"); it.Status != pb.ItemOK || it.Reason != "truncated: timeout" {
		t.Errorf("stats item %+v, want ok and truncated: timeout", it)
	}
	if b, _ := a.File("proxies/gw/slow/stats.txt"); string(b) != "server.live: 1\n[kgwctl: truncated: timeout]\n" {
		t.Errorf("stats.txt = %q", b)
	}
	if it := manifestItem(t, m, "proxies/gw/slow/certs.json"); it.Status != pb.ItemTimeout {
		t.Errorf("certs item %+v, want timeout without a request", it)
	}
}

// TestProxyAdminUnstarted: a run that ends before any capture starts
// sends no request.
func TestProxyAdminUnstarted(t *testing.T) {
	rs := []pb.Replica{proxyReplica("a", corev1.PodRunning, true)}
	d := &fakeAdmin{reply: adminBodies}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newRecordSink()
	if err := collect.ProxyAdmin(d, false).Collect(ctx, pb.Env{Kube: kubefake.NewClientset(), Replicas: rs}, s); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := d.requests(); len(got) != 0 {
		t.Errorf("requests %v, want none", got)
	}
}
