package collect_test

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// scrapeBodyCanary is in every error body the fake API server sends.
const scrapeBodyCanary = "kgwctl-canary-metrics-error-body"

// metricsServer is a fake API server for pods/proxy metrics: the Pod name
// picks the response. It records each request as "METHOD path".
func metricsServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		mu.Unlock()
		pod := ""
		if _, rest, ok := strings.Cut(r.URL.Path, "/pods/http:"); ok {
			pod, _, _ = strings.Cut(rest, ":")
		}
		switch pod {
		case "ok":
			fmt.Fprint(w, "# TYPE up gauge\nup 1\n")
		case "redirect":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, scrapeBodyCanary)
		case "refused":
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "error trying to reach service: dial tcp 10.0.0.1:9091: connect: connection refused "+scrapeBodyCanary)
		case "unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, scrapeBodyCanary)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, scrapeBodyCanary)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reqs...)
	}
}

// TestStatusScraperRoute: one GET of pods/http:<pod>:<port>/proxy/metrics
// through the guard; a redirect is not followed; a 403 is a typed
// *HTTPStatusError that the Sink records as denied; a 503 whose body said
// "connection refused" sets Refused; no body text reaches an error.
func TestStatusScraperRoute(t *testing.T) {
	srv, reqs := metricsServer(t)
	s, err := collect.StatusScraper(ib.Guard(&rest.Config{Host: srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	pod := func(name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: name}}
	}

	rc, err := s.Scrape(context.Background(), pod("ok"), 9091)
	if err != nil {
		t.Fatalf("ok: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "# TYPE up gauge\nup 1\n" {
		t.Errorf("ok body %q", b)
	}
	if got := reqs(); len(got) != 1 || got[0] != "GET /api/v1/namespaces/gw/pods/http:ok:9091/proxy/metrics" {
		t.Errorf("requests %v", got)
	}

	for _, tc := range []struct {
		pod     string
		code    int
		refused bool
		status  pb.ItemStatus
	}{
		{"redirect", http.StatusFound, false, pb.ItemFailed},
		{"denied", http.StatusForbidden, false, pb.ItemDenied},
		{"refused", http.StatusServiceUnavailable, true, pb.ItemFailed},
		{"unavailable", http.StatusServiceUnavailable, false, pb.ItemFailed},
	} {
		t.Run(tc.pod, func(t *testing.T) {
			before := len(reqs())
			rc, err := s.Scrape(context.Background(), pod(tc.pod), 9091)
			if err == nil {
				_ = rc.Close()
				t.Fatal("no error")
			}
			var he *collect.HTTPStatusError
			if !errors.As(err, &he) || he.Code != tc.code || he.Refused != tc.refused {
				t.Fatalf("error %#v, want *HTTPStatusError{Code: %d, Refused: %t}", err, tc.code, tc.refused)
			}
			if ib.Refused(err) != tc.refused {
				t.Errorf("ib.Refused = %t, want %t", ib.Refused(err), tc.refused)
			}
			if got := ib.ItemStatus(err); got != tc.status {
				t.Errorf("item status %s, want %s", got, tc.status)
			}
			if strings.Contains(err.Error(), scrapeBodyCanary) || strings.Contains(err.Error(), "connection refused") {
				t.Errorf("error %q carries body text", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(tc.code)) {
				t.Errorf("error %q lacks the status", err)
			}
			// One GET each; the redirect target is never requested.
			if got := reqs()[before:]; len(got) != 1 || !strings.HasSuffix(got[0], "/pods/http:"+tc.pod+":9091/proxy/metrics") {
				t.Errorf("requests %v", got)
			}
		})
	}
}
