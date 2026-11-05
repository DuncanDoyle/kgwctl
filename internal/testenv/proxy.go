package testenv

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Request kinds a Match selects.
const (
	// KindAcquire is a classic LIST, or a WATCH with sendInitialEvents=true.
	KindAcquire = "acquire"
	// KindWatch is a WATCH without sendInitialEvents=true.
	KindWatch = "watch"
	// KindDiscovery is a GET under /api, /apis or /version that names no resource.
	KindDiscovery = "discovery"
)

// holdAfterList is how long HoldWatchThenStatus keeps a watch open after the matching LIST.
const holdAfterList = 200 * time.Millisecond

// Match selects requests by kind, resource and label selector.
type Match struct {
	// Kind is KindAcquire, KindWatch or KindDiscovery.
	Kind string
	// Resource is "pods" for the core group and "<resource>.<group>" otherwise.
	// For discovery it is "*" for every request, or the group version ("v1",
	// "gateway.networking.k8s.io/v1").
	Resource string
	// LabelSelector is compared as a parsed selector; "" matches any.
	LabelSelector string
}

// Action is the fault applied to a matching request. Delay runs first, then
// Until; then Status, if set, answers instead of envtest.
type Action struct {
	Delay time.Duration
	// Until, if set, holds the request until the channel is closed.
	Until      <-chan struct{}
	Status     int
	RetryAfter int // seconds, sent with Status
	// HoldWatchThenStatus keeps a plain watch open until the matching LIST has
	// completed plus 200ms, then answers with this status.
	HoldWatchThenStatus int
}

// RuleHits counts the requests a rule applied to.
type RuleHits struct{ n atomic.Int64 }

// N returns the number of requests the rule applied to so far.
func (h *RuleHits) N() int { return int(h.n.Load()) }

type rule struct {
	m        Match
	selector string // canonical form of m.LabelSelector
	a        Action
	hits     *RuleHits
}

// request is the classified form of one incoming request.
type request struct {
	kind     string // a Kind constant, or "" when no rule can match
	resource string
	selector string // canonical label selector
}

// Proxy is an httptest reverse proxy in front of envtest that injects faults.
// Requests reach envtest as the admin user.
type Proxy struct {
	srv *httptest.Server
	rp  *httputil.ReverseProxy

	mu       sync.Mutex
	rules    []*rule
	requests []string
	// listed holds, per resource and selector, when an acquisition last
	// completed and a channel closed at the next completion.
	listed map[string]*listState

	openWatches atomic.Int64
	watchesSeen atomic.Int64
	connMu      sync.Mutex
	conns       map[net.Conn]http.ConnState
}

type listState struct {
	done time.Time
	next chan struct{}
}

func newProxy(target *url.URL, transport http.RoundTripper) *Proxy {
	p := &Proxy{listed: map[string]*listState{}}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		Transport:     transport,
		FlushInterval: -1, // flush every chunk, so watch events stream through
		// Cancelled client requests are expected; their 502 needs no log line.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	p.conns = map[net.Conn]http.ConnState{}
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.srv.Config.ConnState = p.trackConn
	p.srv.Start()
	return p
}

// URL returns the proxy's base URL.
func (p *Proxy) URL() string { return p.srv.URL }

// Rule adds a fault. The first matching rule in the order added applies.
func (p *Proxy) Rule(m Match, a Action) *RuleHits {
	sel := ""
	if m.LabelSelector != "" {
		s, err := labels.Parse(m.LabelSelector)
		if err != nil {
			panic(fmt.Sprintf("testenv: rule selector %q: %v", m.LabelSelector, err))
		}
		sel = s.String()
	}
	r := &rule{m: m, selector: sel, a: a, hits: &RuleHits{}}
	p.mu.Lock()
	p.rules = append(p.rules, r)
	p.mu.Unlock()
	return r.hits
}

// Requests returns every request seen so far, as "METHOD /path?query".
func (p *Proxy) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// OpenWatches returns the number of watch requests currently open.
func (p *Proxy) OpenWatches() int { return int(p.openWatches.Load()) }

// WatchesSeen returns the number of watch requests received so far.
func (p *Proxy) WatchesSeen() int { return int(p.watchesSeen.Load()) }

// OpenConns returns the number of client connections not yet closed or hijacked.
func (p *Proxy) OpenConns() int {
	p.connMu.Lock()
	defer p.connMu.Unlock()
	return len(p.conns)
}

func (p *Proxy) trackConn(c net.Conn, s http.ConnState) {
	p.connMu.Lock()
	defer p.connMu.Unlock()
	if s == http.StateClosed || s == http.StateHijacked {
		delete(p.conns, c)
		return
	}
	p.conns[c] = s
}

// unmatched returns the rules that never applied.
func (p *Proxy) unmatched() []Match {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Match
	for _, r := range p.rules {
		if r.hits.N() == 0 {
			out = append(out, r.m)
		}
	}
	return out
}

func (p *Proxy) close() {
	p.srv.CloseClientConnections()
	p.srv.Close()
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	req := classify(r)
	p.mu.Lock()
	p.requests = append(p.requests, r.Method+" "+r.URL.RequestURI())
	var match *rule
	for _, ru := range p.rules {
		if ru.matches(req) {
			match = ru
			break
		}
	}
	p.mu.Unlock()

	if r.URL.Query().Get("watch") == "true" || r.URL.Query().Get("watch") == "1" {
		p.openWatches.Add(1)
		p.watchesSeen.Add(1)
		defer p.openWatches.Add(-1)
	}
	if match != nil {
		match.hits.n.Add(1)
		if !p.apply(w, r, req, match.a) {
			return
		}
	}
	if req.kind == KindAcquire {
		defer p.acquired(req)
	}
	p.rp.ServeHTTP(w, r)
}

// apply runs a's faults outside any lock and reports whether the request
// should still be forwarded.
func (p *Proxy) apply(w http.ResponseWriter, r *http.Request, req request, a Action) bool {
	if a.Delay > 0 {
		t := time.NewTimer(a.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-r.Context().Done():
			return false
		}
	}
	if a.Until != nil {
		select {
		case <-a.Until:
		case <-r.Context().Done():
			return false
		}
	}
	if a.HoldWatchThenStatus != 0 && req.kind == KindWatch {
		if !p.waitForList(r, req) {
			return false
		}
		writeStatus(w, req, a.HoldWatchThenStatus, 0)
		return false
	}
	if a.Status != 0 {
		writeStatus(w, req, a.Status, a.RetryAfter)
		return false
	}
	return true
}

func listKey(req request) string { return req.resource + "?" + req.selector }

// acquired records that an acquisition for req's resource and selector completed.
func (p *Proxy) acquired(req request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.listed[listKey(req)]
	if st == nil {
		st = &listState{next: make(chan struct{})}
		p.listed[listKey(req)] = st
	}
	st.done = time.Now()
	close(st.next)
	st.next = make(chan struct{})
}

// waitForList blocks until the matching LIST completed plus holdAfterList,
// or the request ends; it reports whether the hold ran to completion.
func (p *Proxy) waitForList(r *http.Request, req request) bool {
	for {
		p.mu.Lock()
		st := p.listed[listKey(req)]
		if st == nil {
			st = &listState{next: make(chan struct{})}
			p.listed[listKey(req)] = st
		}
		done, next := st.done, st.next
		p.mu.Unlock()
		if !done.IsZero() {
			t := time.NewTimer(time.Until(done.Add(holdAfterList)))
			defer t.Stop()
			select {
			case <-t.C:
				return true
			case <-r.Context().Done():
				return false
			}
		}
		select {
		case <-next:
		case <-r.Context().Done():
			return false
		}
	}
}

func (ru *rule) matches(req request) bool {
	if req.kind == "" || ru.m.Kind != req.kind {
		return false
	}
	if ru.m.Resource != "*" && ru.m.Resource != req.resource {
		return false
	}
	return ru.selector == "" || ru.selector == req.selector
}

// classify parses the request path the way the API server lays it out:
// /api/v1[/namespaces/<ns>]/<resource>[/<name>] and the same under /apis/<group>/<version>.
func classify(r *http.Request) request {
	if r.Method != http.MethodGet {
		return request{}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var group string
	var rest []string
	switch {
	case parts[0] == "version" || parts[0] == "openapi":
		return request{kind: KindDiscovery, resource: parts[0]}
	case parts[0] == "api" && len(parts) <= 2:
		return request{kind: KindDiscovery, resource: strings.Join(parts[1:], "/")}
	case parts[0] == "apis" && len(parts) <= 3:
		return request{kind: KindDiscovery, resource: strings.Join(parts[1:], "/")}
	case parts[0] == "api":
		rest = parts[2:]
	case parts[0] == "apis":
		group, rest = parts[1], parts[3:]
	default:
		return request{}
	}
	// A namespaced collection: namespaces/<ns>/<resource>.
	if len(rest) >= 3 && rest[0] == "namespaces" {
		rest = rest[2:]
	}
	if len(rest) != 1 {
		return request{} // a named object or subresource
	}
	resource := rest[0]
	if group != "" {
		resource += "." + group
	}
	q := r.URL.Query()
	sel := ""
	if s := q.Get("labelSelector"); s != "" {
		if parsed, err := labels.Parse(s); err == nil {
			sel = parsed.String()
		} else {
			sel = s
		}
	}
	kind := KindAcquire
	if w := q.Get("watch"); w == "true" || w == "1" {
		if v, _ := strconv.ParseBool(q.Get("sendInitialEvents")); !v {
			kind = KindWatch
		}
	}
	return request{kind: kind, resource: resource, selector: sel}
}

// writeStatus answers with a metav1.Status the way the API server does, so
// clients decode it into the matching apierrors type.
func writeStatus(w http.ResponseWriter, req request, code, retryAfter int) {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Code:     int32(code),
		Reason:   reasonFor(code),
		Message:  fmt.Sprintf("%s is %s (injected by testenv)", req.resource, strings.ToLower(http.StatusText(code))),
	}
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		st.Details = &metav1.StatusDetails{RetryAfterSeconds: int32(retryAfter)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(st)
}

func reasonFor(code int) metav1.StatusReason {
	switch code {
	case http.StatusUnauthorized:
		return metav1.StatusReasonUnauthorized
	case http.StatusForbidden:
		return metav1.StatusReasonForbidden
	case http.StatusNotFound:
		return metav1.StatusReasonNotFound
	case http.StatusTooManyRequests:
		return metav1.StatusReasonTooManyRequests
	case http.StatusInternalServerError:
		return metav1.StatusReasonInternalError
	case http.StatusServiceUnavailable:
		return metav1.StatusReasonServiceUnavailable
	case http.StatusGatewayTimeout:
		return metav1.StatusReasonTimeout
	}
	return metav1.StatusReasonUnknown
}

// ProxyPodSelector returns the label selector the cache sends on its proxy
// Pod requests: that of the first registered detector that selects proxy
// Pods, or "" when none does. Use it as Match.LabelSelector.
func ProxyPodSelector(reg *extension.Registry) string {
	for _, d := range reg.Detectors {
		if pd, ok := d.(install.ProxyPodDetector); ok {
			return pd.ProxyPodSelector().String()
		}
	}
	return ""
}
