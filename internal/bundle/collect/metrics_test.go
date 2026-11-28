package collect_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// okMetrics is a valid Prometheus exposition.
const okMetrics = "# HELP up Whether the target is up.\n# TYPE up gauge\nup 1\n"

// funcScraper is a Scraper that answers by Pod name and records each
// scrape as "<ns>/<pod>:<port>".
type funcScraper struct {
	mu    sync.Mutex
	calls []string
	reply func(pod string) (string, error)
}

func (f *funcScraper) Scrape(_ context.Context, pod *corev1.Pod, port int32) (io.ReadCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s/%s:%d", pod.Namespace, pod.Name, port))
	f.mu.Unlock()
	body, err := f.reply(pod.Name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// podsOf returns a fake clientset holding a Pod per replica, for the Sink's re-read.
func podsOf(rs ...pb.Replica) *kubefake.Clientset {
	var objs []runtime.Object
	for _, r := range rs {
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: r.Pod, UID: r.UID}})
	}
	return kubefake.NewClientset(objs...)
}

// runMetrics runs both metrics collectors through the real writer.
func runMetrics(t *testing.T, env pb.Env, s collect.Scraper) (ib.Manifest, bundletest.Archive) {
	t.Helper()
	r := newLogsRun(t)
	m, err := ib.Run(context.Background(), []pb.Collector{collect.ControllerMetrics(s), collect.ProxyMetrics(s)},
		env, r.w, ib.Manifest{}, ib.RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return m, bundletest.Extract(t, &r.buf)
}

func proxyReplica(pod string, phase corev1.PodPhase, declared bool) pb.Replica {
	r := replica("gw", pod, pb.RoleProxy, pb.IdentityAnnotation, phase, pb.Container{Name: "kgateway-proxy"})
	r.MetricsPort, r.MetricsPortDeclared = ib.ProxyMetricsPort, declared
	return r
}

// TestMetricsStatuses: a scraped replica is ok and verified; a 403 is
// denied; a refused connection is disabled only when the Pod declares no
// metrics port, else failed; a Pod not running is skipped unscraped; an
// unverified replica gets nothing. Controllers use their install's port,
// proxies 9091.
func TestMetricsStatuses(t *testing.T) {
	ctrl := replica("kgateway-system", "ctrl-0", pb.RoleController, pb.IdentitySelector, corev1.PodRunning, pb.Container{Name: "kgateway"})
	ctrl.MetricsPort, ctrl.MetricsPortDeclared = 9555, true
	unverified := proxyReplica("unverified", corev1.PodRunning, false)
	unverified.Identity = pb.IdentityUnverified
	// Proxies try 9091 whatever the replica's recorded port.
	okProxy := proxyReplica("ok", corev1.PodRunning, true)
	okProxy.MetricsPort = 0
	rs := []pb.Replica{ctrl,
		okProxy,
		proxyReplica("denied", corev1.PodRunning, true),
		proxyReplica("off", corev1.PodRunning, false),
		proxyReplica("broken", corev1.PodRunning, true),
		proxyReplica("pending", corev1.PodPending, true),
		unverified,
	}
	s := &funcScraper{reply: func(pod string) (string, error) {
		switch pod {
		case "denied":
			return "", &collect.HTTPStatusError{Code: http.StatusForbidden, Reason: string(metav1.StatusReasonForbidden)}
		case "off", "broken":
			return "", &collect.HTTPStatusError{Code: http.StatusServiceUnavailable, Reason: string(metav1.StatusReasonServiceUnavailable), Refused: true}
		}
		return okMetrics, nil
	}}
	m, a := runMetrics(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, s)

	for p, want := range map[string]struct {
		status pb.ItemStatus
		reason string
	}{
		"controller/kgateway-system/ctrl-0/metrics.txt": {pb.ItemOK, ""},
		"proxies/gw/ok/metrics.txt":                     {pb.ItemOK, ""},
		"proxies/gw/denied/metrics.txt":                 {pb.ItemDenied, ""},
		"proxies/gw/off/metrics.txt":                    {pb.ItemDisabled, string(pb.OmitNoMetricsPort)},
		"proxies/gw/broken/metrics.txt":                 {pb.ItemFailed, ""},
		"proxies/gw/pending/metrics.txt":                {pb.ItemSkipped, string(pb.OmitNotRunning)},
	} {
		it := manifestItem(t, m, p)
		if it.Status != want.status || (want.reason != "" && it.Reason != want.reason) {
			t.Errorf("%s: item %+v, want %s %s", p, it, want.status, want.reason)
		}
		if it.Status == pb.ItemOK {
			if it.Source.Identity != pb.IdentityVerified || it.Source.Operation != pb.OpMetrics {
				t.Errorf("%s: source %+v, want verified metrics", p, it.Source)
			}
			if b, _ := a.File(p); string(b) != okMetrics {
				t.Errorf("%s = %q", p, b)
			}
		}
	}
	for _, it := range m.Items {
		if strings.Contains(it.Path, "unverified") {
			t.Errorf("unverified replica has item %+v", it)
		}
	}
	wantCalls := []string{"gw/broken:9091", "gw/denied:9091", "gw/off:9091", "gw/ok:9091", "kgateway-system/ctrl-0:9555"}
	if got := slices.Sorted(slices.Values(s.calls)); !slices.Equal(got, wantCalls) {
		t.Errorf("scrapes %v, want %v", got, wantCalls)
	}
	names := map[string]bool{}
	for _, c := range m.Collectors {
		names[c.Name] = true
	}
	if got := slices.Sorted(maps.Keys(names)); !slices.Equal(got, []string{"controller-metrics", "proxy-metrics"}) {
		t.Errorf("collectors %v", got)
	}
}

// TestMetricsStockMarkersNoPort: a proxy Pod with only the stock label and
// full-name annotation, declaring no 9091, is found by the inventory and
// scraped ok over pods/proxy.
func TestMetricsStockMarkersNoPort(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: "ok", UID: podUID("ok"),
			Labels:      map[string]string{ib.ProxyLabel: ib.ProxyLabelValue},
			Annotations: map[string]string{ib.GatewayFullNameAnnotation: "gw"}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "kgateway-proxy", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	kube := kubefake.NewClientset(pod)
	rs, errs := ib.Inventory(context.Background(), kube, pb.Scope{}, ib.GatewaySet{{Namespace: "gw", Name: "gw"}: ""}, nil)
	if len(errs) != 0 || len(rs) != 1 || rs[0].MetricsPortDeclared {
		t.Fatalf("inventory %+v, %v", rs, errs)
	}
	srv, reqs := metricsServer(t)
	s, err := collect.StatusScraper(ib.Guard(&rest.Config{Host: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	m, a := runMetrics(t, pb.Env{Kube: kube, Replicas: rs}, s)
	if it := manifestItem(t, m, "proxies/gw/ok/metrics.txt"); it.Status != pb.ItemOK || it.Source.Identity != pb.IdentityVerified {
		t.Errorf("item %+v, want ok and verified", it)
	}
	if b, _ := a.File("proxies/gw/ok/metrics.txt"); string(b) != "# TYPE up gauge\nup 1\n" {
		t.Errorf("metrics.txt = %q", b)
	}
	if got := reqs(); !slices.Equal(got, []string{"GET /api/v1/namespaces/gw/pods/http:ok:9091/proxy/metrics"}) {
		t.Errorf("requests %v", got)
	}
}

// TestMetricsArchive: in the extracted archive, a non-Prometheus body is
// dropped with an error and a Bearer label value is redacted.
func TestMetricsArchive(t *testing.T) {
	const (
		htmlCanary   = "kgwctl-canary-metrics-html"
		bearerCanary = "kgwctl-canary-metrics-bearer"
	)
	rs := []pb.Replica{proxyReplica("html", corev1.PodRunning, true), proxyReplica("bearer", corev1.PodRunning, true)}
	s := &funcScraper{reply: func(pod string) (string, error) {
		if pod == "html" {
			return "<html><body>" + htmlCanary + "</body></html>\n", nil
		}
		return "# TYPE req counter\nreq{auth=\"Bearer " + bearerCanary + "\"} 1\n", nil
	}}
	m, a := runMetrics(t, pb.Env{Kube: podsOf(rs...), Replicas: rs}, s)

	html := manifestItem(t, m, "proxies/gw/html/metrics.txt")
	if html.Status != pb.ItemFailed || html.Reason != ib.ReasonNotPrometheus {
		t.Errorf("html item %+v, want failed: %s", html, ib.ReasonNotPrometheus)
	}
	if _, ok := a.File("proxies/gw/html/metrics.txt"); ok {
		t.Error("html body has an archive member")
	}
	if eb, _ := a.File(pb.ErrorsFile); !strings.Contains(string(eb), "proxies/gw/html/metrics.txt") {
		t.Errorf("errors.txt lacks the html item:\n%s", eb)
	}
	b, _ := a.File("proxies/gw/bearer/metrics.txt")
	if want := "# TYPE req counter\nreq{auth=\"Bearer [redacted]\"} 1\n"; string(b) != want {
		t.Errorf("bearer metrics.txt = %q, want %q", b, want)
	}
	if it := manifestItem(t, m, "proxies/gw/bearer/metrics.txt"); it.Status != pb.ItemOK || it.Redactions == 0 {
		t.Errorf("bearer item %+v, want ok with redactions", it)
	}
	bundletest.AssertNoCanary(t, a, []string{htmlCanary, bearerCanary})
}
