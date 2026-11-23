package bundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// recorder is an API server stub that records each request's method and path.
type recorder struct {
	mu   sync.Mutex
	reqs []string
	h    http.HandlerFunc
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Method+" "+req.URL.Path)
	r.mu.Unlock()
	r.h(w, req)
}

func (r *recorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

// guardedClient serves h and returns a clientset built from Guard.
func guardedClient(t *testing.T, h http.HandlerFunc) (kubernetes.Interface, *recorder) {
	t.Helper()
	rec := &recorder{h: h}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	kube, err := kubernetes.NewForConfig(Guard(&rest.Config{Host: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	return kube, rec
}

func emptyPodList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","metadata":{},"items":[]}`)
}

func TestGuardRefuses(t *testing.T) {
	kube, rec := guardedClient(t, emptyPodList)
	ctx := context.Background()
	core := kube.CoreV1()
	calls := map[string]func() error{}
	for _, ns := range []string{"team-a", ""} {
		calls["list secrets "+ns] = func() error { _, err := core.Secrets(ns).List(ctx, metav1.ListOptions{}); return err }
		calls["get secret "+ns] = func() error { _, err := core.Secrets(ns).Get(ctx, "s", metav1.GetOptions{}); return err }
		calls["watch secrets "+ns] = func() error { _, err := core.Secrets(ns).Watch(ctx, metav1.ListOptions{}); return err }
		calls["list configmaps "+ns] = func() error { _, err := core.ConfigMaps(ns).List(ctx, metav1.ListOptions{}); return err }
		calls["get configmap "+ns] = func() error { _, err := core.ConfigMaps(ns).Get(ctx, "c", metav1.GetOptions{}); return err }
		calls["watch configmaps "+ns] = func() error { _, err := core.ConfigMaps(ns).Watch(ctx, metav1.ListOptions{}); return err }
	}
	// The legacy watch path names the resource after /watch.
	calls["legacy watch secrets"] = func() error {
		return core.RESTClient().Get().AbsPath("/api/v1/watch/namespaces/team-a/secrets").Do(ctx).Error()
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, pb.ErrForbiddenKind) {
			t.Errorf("%s: err %v, want ErrForbiddenKind", name, err)
		}
	}
	if reqs := rec.requests(); len(reqs) != 0 {
		t.Fatalf("server saw %v, want no request", reqs)
	}
	// A Namespace named secrets is not the secrets resource.
	if _, err := core.Pods("team-a").List(ctx, metav1.ListOptions{}); err != nil {
		t.Errorf("pods list: %v", err)
	}
	if err := core.RESTClient().Get().AbsPath("/api/v1/namespaces/secrets").Do(ctx).Error(); errors.Is(err, pb.ErrForbiddenKind) {
		t.Error("get Namespace secrets refused")
	}
	if got := len(rec.requests()); got != 2 {
		t.Errorf("server saw %d requests, want 2", got)
	}
}

func TestGuardSanitizesBodies(t *testing.T) {
	const canary = "kgwctl-canary-guard-body"
	refused := "dial tcp 10.1.2.3:9091: connect: connection refused " + canary
	kube, _ := guardedClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pods/forbidden"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":%q,"reason":"Forbidden","code":403}`, canary)
		case strings.HasSuffix(r.URL.Path, "/pods/broken"):
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, canary)
		case strings.Contains(r.URL.Path, "/pods/refused"):
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, refused)
		case strings.Contains(r.URL.Path, "/pods/late"):
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, strings.Repeat("x", 4<<10)+refused)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	noCanary := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if strings.Contains(fmt.Sprintf("%v %#v", err, err), canary) || strings.Contains(err.Error(), "10.1.2.3") {
			t.Errorf("%s: error carries the body: %v", name, err)
		}
	}

	_, err := kube.CoreV1().Pods("team-a").Get(ctx, "forbidden", metav1.GetOptions{})
	noCanary("403", err)
	if !apierrors.IsForbidden(err) || statusCode(err) != 403 {
		t.Errorf("403: got %v (code %d), want Forbidden 403", err, statusCode(err))
	}
	_, err = kube.CoreV1().Pods("team-a").Get(ctx, "broken", metav1.GetOptions{})
	noCanary("500", err)
	if !apierrors.IsInternalError(err) || statusCode(err) != 500 {
		t.Errorf("500: got %v (code %d), want InternalError 500", err, statusCode(err))
	}

	proxy := func(pod string) error {
		return kube.CoreV1().RESTClient().Get().Namespace("team-a").Resource("pods").Name(pod + ":9091").
			SubResource("proxy").Suffix("metrics").Do(ctx).Error()
	}
	err = proxy("refused")
	noCanary("refused", err)
	if !Refused(err) || statusCode(err) != 503 {
		t.Errorf("refused: got %v (code %d), want the refused cause on 503", err, statusCode(err))
	}
	err = proxy("late")
	noCanary("late", err)
	if Refused(err) || statusCode(err) != 502 {
		t.Errorf("phrase past 4 KiB: got %v (code %d), want 502 without the refused cause", err, statusCode(err))
	}
}

// TestGuardPassesRedirects: 3xx responses, 304 included, pass untouched.
func TestGuardPassesRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/moved") {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
			io.WriteString(w, "moved body")
			return
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)
	rt, err := rest.TransportFor(Guard(&rest.Config{Host: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/api/v1/namespaces/a/pods/p/proxy/moved": 302, "/api/v1/pods": 304} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want || resp.Header.Get("Content-Type") == "application/json" {
			t.Errorf("%s: status %d, content type %q; want %d untouched", path, resp.StatusCode, resp.Header.Get("Content-Type"), want)
		}
		if want == 302 && (string(body) != "moved body" || resp.Header.Get("Location") != "/elsewhere") {
			t.Errorf("%s: body %q, location %q; want them untouched", path, body, resp.Header.Get("Location"))
		}
	}
}

func statusCode(err error) int32 {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		return st.Status().Code
	}
	return 0
}
