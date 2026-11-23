package collect_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// logSince is the window start of the log tests, whole seconds as sinceTime is sent.
var logSince = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// funcLogs is a LogSource that calls open and records each request's options.
type funcLogs struct {
	mu   sync.Mutex
	reqs map[string]corev1.PodLogOptions
	open func(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

func (f *funcLogs) Stream(ctx context.Context, pod types.NamespacedName, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	if f.reqs == nil {
		f.reqs = map[string]corev1.PodLogOptions{}
	}
	f.reqs[fmt.Sprintf("%s/%s/%s/%t", pod.Namespace, pod.Name, opts.Container, opts.Previous)] = *opts
	f.mu.Unlock()
	return f.open(ctx, pod, opts)
}

// podUID is a fixed UUID per Pod name, as the writer records only UUIDs.
func podUID(pod string) types.UID {
	sum := sha256.Sum256([]byte(pod))
	h := hex.EncodeToString(sum[:])
	return types.UID(fmt.Sprintf("%s-%s-4%s-8%s-%s", h[0:8], h[8:12], h[12:15], h[15:18], h[18:30]))
}

func replica(ns, pod string, role pb.Role, id pb.Identity, phase corev1.PodPhase, cs ...pb.Container) pb.Replica {
	return pb.Replica{Namespace: ns, Pod: pod, UID: podUID(pod), Role: role, Identity: id, Phase: phase, Containers: cs}
}

// logsRun is one run of the logs collector through the real writer.
type logsRun struct {
	w   *ib.Writer
	buf bytes.Buffer
}

func newLogsRun(t *testing.T) *logsRun {
	t.Helper()
	l, err := ib.DefaultLimits(ib.MinSize)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ib.NewSpool(t.TempDir(), l.RAM, l.MaxSize)
	if err != nil {
		t.Fatal(err)
	}
	r := &logsRun{}
	r.w = ib.NewWriter(ib.Stdout(&r.buf), sp, "kgwctl-bundle-test", l, redact.Options{}, "", time.Now)
	return r
}

// run runs the collector; it is safe to call from any goroutine.
func (r *logsRun) run(ctx context.Context, env pb.Env, src collect.LogSource) (ib.Manifest, error) {
	return ib.Run(ctx, []pb.Collector{collect.Logs(src)}, env, r.w, ib.Manifest{}, ib.RunOptions{})
}

// runLogs runs the logs collector and returns the manifest and the extracted archive.
func runLogs(t *testing.T, ctx context.Context, env pb.Env, src collect.LogSource) (ib.Manifest, bundletest.Archive) {
	t.Helper()
	r := newLogsRun(t)
	m, err := r.run(ctx, env, src)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return m, bundletest.Extract(t, &r.buf)
}

func manifestItem(t *testing.T, m ib.Manifest, path string) ib.Item {
	t.Helper()
	for _, it := range m.Items {
		if it.Path == path {
			return it
		}
	}
	t.Fatalf("no manifest item at %s: %+v", path, m.Items)
	return ib.Item{}
}

// TestKubeLogSourceQuery: each stream is one GET of the Pod's log
// subresource with the window's sinceTime, timestamps, tailLines and
// limitBytes; previous=true only for a restarted container.
func TestKubeLogSourceQuery(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		fmt.Fprint(w, "2026-11-22T10:00:01Z hello\n")
	}))
	t.Cleanup(srv.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	env := pb.Env{Window: pb.Window{Start: logSince.Add(time.Hour), Since: logSince, Tail: 5000},
		Replicas: []pb.Replica{replica("gw", "gw-0", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodRunning,
			pb.Container{Name: "init", Init: true, Restarts: 1}, pb.Container{Name: "envoy"})}}
	s := newRecordSink()
	if err := collect.Logs(collect.KubeLogSource(kube)).Collect(context.Background(), env, s); err != nil {
		t.Fatal(err)
	}
	if len(s.errs) != 0 {
		t.Fatalf("errors: %v", s.errs)
	}
	if got := s.text["proxies/gw/gw-0/logs/envoy.log"]; got != "2026-11-22T10:00:01Z hello\n" {
		t.Errorf("envoy.log = %q", got)
	}
	want := map[string]bool{"envoy/false": false, "init/false": false, "init/true": true}
	if len(reqs) != len(want) {
		t.Fatalf("requests %v, want %d", reqs, len(want))
	}
	for _, r := range reqs {
		path, query, _ := strings.Cut(strings.TrimPrefix(r, "GET "), "?")
		if path != "/api/v1/namespaces/gw/pods/gw-0/log" {
			t.Errorf("request %s", r)
			continue
		}
		q := parseQuery(t, query)
		key := q["container"] + "/" + strconv.FormatBool(q["previous"] == "true")
		prev, ok := want[key]
		if !ok {
			t.Errorf("unexpected request %s", r)
			continue
		}
		delete(want, key)
		wantQ := map[string]string{"container": q["container"], "sinceTime": "2026-11-22T10:00:00Z", "timestamps": "true",
			"tailLines": "5000", "limitBytes": strconv.Itoa(10<<20 + 64<<10)}
		if prev {
			wantQ["previous"] = "true"
		}
		if fmt.Sprint(q) != fmt.Sprint(wantQ) {
			t.Errorf("query %v, want %v", q, wantQ)
		}
	}
}

func parseQuery(t *testing.T, raw string) map[string]string {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k := range v {
		out[k] = v.Get(k)
	}
	return out
}

// TestKubeLogSourceErrorBody: a failed stream keeps its status, never the
// response body, whether the body is plain text or a Status.
func TestKubeLogSourceErrorBody(t *testing.T) {
	const canary = "kgwctl-canary-log-error-body"
	status, _ := json.Marshal(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status: metav1.StatusFailure, Code: 500, Reason: metav1.StatusReasonInternalError, Message: canary})
	for name, body := range map[string]string{"text": canary, "status": string(status)} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if name == "status" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, body)
			}))
			t.Cleanup(srv.Close)
			kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = collect.KubeLogSource(kube).Stream(context.Background(), types.NamespacedName{Namespace: "gw", Name: "gw-0"},
				&corev1.PodLogOptions{Container: "envoy"})
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), canary) || !strings.Contains(err.Error(), "500") {
				t.Errorf("error %q, want the status without the body", err)
			}
			var st apierrors.APIStatus
			if !errors.As(err, &st) || st.Status().Code != 500 {
				t.Errorf("error %#v lost its status", err)
			}
		})
	}
}

// TestLogsPerReplica: every inventory container gets a log, init containers
// included, and .previous.log only after a restart. A Pod that is not running
// still gets logs; an unverified replica gets none.
func TestLogsPerReplica(t *testing.T) {
	src := &funcLogs{open: func(context.Context, types.NamespacedName, *corev1.PodLogOptions) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("line\n")), nil
	}}
	env := pb.Env{Window: pb.Window{Since: logSince, Tail: 10}, Replicas: []pb.Replica{
		replica("kgateway-system", "ctrl-0", pb.RoleController, pb.IdentitySelector, corev1.PodRunning,
			pb.Container{Name: "setup", Init: true, Restarts: 1}, pb.Container{Name: "kgateway"}),
		replica("gw", "gw-0", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodRunning, pb.Container{Name: "kgateway-proxy", Restarts: 2}),
		replica("gw", "gw-pending", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodPending, pb.Container{Name: "kgateway-proxy"}),
		replica("gw", "gw-unverified", pb.RoleProxy, pb.IdentityUnverified, corev1.PodRunning, pb.Container{Name: "kgateway-proxy"}),
	}}
	s := newRecordSink()
	if err := collect.Logs(src).Collect(context.Background(), env, s); err != nil {
		t.Fatal(err)
	}
	if len(s.errs) != 0 || len(s.omits) != 0 {
		t.Fatalf("errors %v, omissions %v", s.errs, s.omits)
	}
	src1 := func(ns, pod, c string, op pb.Op) pb.Source {
		return pb.Source{Namespace: ns, Pod: pod, UID: podUID(pod), Container: c, Operation: op}
	}
	want := map[string]pb.Source{
		"controller/kgateway-system/ctrl-0/logs/setup.log":          src1("kgateway-system", "ctrl-0", "setup", pb.OpLog),
		"controller/kgateway-system/ctrl-0/logs/setup.previous.log": src1("kgateway-system", "ctrl-0", "setup", pb.OpPreviousLog),
		"controller/kgateway-system/ctrl-0/logs/kgateway.log":       src1("kgateway-system", "ctrl-0", "kgateway", pb.OpLog),
		"proxies/gw/gw-0/logs/kgateway-proxy.log":                   src1("gw", "gw-0", "kgateway-proxy", pb.OpLog),
		"proxies/gw/gw-0/logs/kgateway-proxy.previous.log":          src1("gw", "gw-0", "kgateway-proxy", pb.OpPreviousLog),
		"proxies/gw/gw-pending/logs/kgateway-proxy.log":             src1("gw", "gw-pending", "kgateway-proxy", pb.OpLog),
	}
	if got := slices.Sorted(maps.Keys(s.text)); len(got) != len(want) {
		t.Errorf("logs %v, want %d", got, len(want))
	}
	for p, w := range want {
		if s.srcs[p] != w {
			t.Errorf("%s: source %+v, want %+v", p, s.srcs[p], w)
		}
	}
	// Streams by namespace/pod/container/previous.
	wantReqs := []string{"gw/gw-0/kgateway-proxy/false", "gw/gw-0/kgateway-proxy/true", "gw/gw-pending/kgateway-proxy/false",
		"kgateway-system/ctrl-0/kgateway/false", "kgateway-system/ctrl-0/setup/false", "kgateway-system/ctrl-0/setup/true"}
	if got := slices.Sorted(maps.Keys(src.reqs)); !slices.Equal(got, wantReqs) {
		t.Errorf("streams %v, want %v", got, wantReqs)
	}
}

// TestLogsReread: the Sink re-reads each Pod after its stream; a Pod that
// stays is verified, one deleted after its stream is gone, and both items
// are ok with no errors.txt line (D-027).
func TestLogsReread(t *testing.T) {
	pod := func(name string) runtime.Object {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: name, UID: podUID(name)}}
	}
	kube := kubefake.NewClientset(pod("stay"), pod("gone"))
	src := &funcLogs{open: func(_ context.Context, p types.NamespacedName, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
		r := io.Reader(strings.NewReader("2026-11-22T10:00:01Z hello\n"))
		if p.Name == "gone" {
			// The Pod is deleted once its stream has been read to the end.
			r = io.MultiReader(r, deleteOnEOF{kube: kube, pod: p})
		}
		return io.NopCloser(r), nil
	}}
	env := pb.Env{Kube: kube, Window: pb.Window{Since: logSince}, Replicas: []pb.Replica{
		replica("gw", "stay", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodRunning, pb.Container{Name: "envoy"}),
		replica("gw", "gone", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodRunning, pb.Container{Name: "envoy"}),
	}}
	m, a := runLogs(t, context.Background(), env, src)
	for pod, id := range map[string]pb.CaptureIdentity{"stay": pb.IdentityVerified, "gone": pb.IdentityGone} {
		it := manifestItem(t, m, "proxies/gw/"+pod+"/logs/envoy.log")
		if it.Status != pb.ItemOK || it.Source.Identity != id {
			t.Errorf("%s: item %+v, want ok and %s", pod, it, id)
		}
	}
	if b, _ := a.File(pb.ErrorsFile); strings.Contains(string(b), "logs") {
		t.Errorf("errors.txt:\n%s", b)
	}
	if c := m.Collectors; len(c) != 1 || c[0].Status != pb.CollectorOK {
		t.Errorf("collectors %+v, want logs ok", c)
	}
}

// deleteOnEOF deletes pod on its first read and then reports EOF.
type deleteOnEOF struct {
	kube *kubefake.Clientset
	pod  types.NamespacedName
}

func (d deleteOnEOF) Read([]byte) (int, error) {
	_ = d.kube.CoreV1().Pods(d.pod.Namespace).Delete(context.Background(), d.pod.Name, metav1.DeleteOptions{})
	return 0, io.EOF
}

// pipeLogs serves one stream from a pipe the test writes, and reports when
// the collector closes it.
type pipeLogs struct {
	pr     *io.PipeReader
	opened chan struct{}
	closed chan struct{}
}

func (p *pipeLogs) Stream(context.Context, types.NamespacedName, *corev1.PodLogOptions) (io.ReadCloser, error) {
	close(p.opened)
	return p, nil
}

func (p *pipeLogs) Read(b []byte) (int, error) { return p.pr.Read(b) }
func (p *pipeLogs) Close() error {
	close(p.closed)
	return p.pr.Close()
}

// TestLogsCancelMidStream: a stream still open at the seal keeps its complete
// lines and a truncation line; the partial last line is dropped, the open
// PEM block stays redacted and is reported as pem-tail. The collector is
// timeout.
func TestLogsCancelMidStream(t *testing.T) {
	const canary = "kgwctl-canary-log-pem-body"
	kube := kubefake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: "gw-0", UID: podUID("gw-0")}})
	pr, pw := io.Pipe()
	src := &pipeLogs{pr: pr, opened: make(chan struct{}), closed: make(chan struct{})}
	env := pb.Env{Kube: kube, Window: pb.Window{Since: logSince}, Replicas: []pb.Replica{
		replica("gw", "gw-0", pb.RoleProxy, pb.IdentityAnnotation, corev1.PodRunning, pb.Container{Name: "envoy"})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := newLogsRun(t)
	type out struct {
		m   ib.Manifest
		err error
	}
	done := make(chan out, 1)
	go func() {
		m, err := run.run(ctx, env, src)
		done <- out{m, err}
	}()

	select {
	case <-src.opened:
	case r := <-done:
		t.Fatalf("run ended before the stream opened: %+v, %v", r.m.Collectors, r.err)
	}
	const ts = "2026-11-22T10:00:01Z "
	if _, err := pw.Write([]byte(ts + "line one\n" + ts + "-----BEGIN RSA PRIVATE KEY-----\n" + ts + canary + "\n")); err != nil {
		t.Fatal(err)
	}
	// Text reads again only after staging every complete line above.
	if _, err := pw.Write([]byte(ts + "partial")); err != nil {
		t.Fatal(err)
	}
	cancel() // the run ends, as at the deadline or on Ctrl-C
	r := <-done
	pw.CloseWithError(errors.New("stream reset"))
	<-src.closed
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	a := bundletest.Extract(t, &run.buf)

	p := "proxies/gw/gw-0/logs/envoy.log"
	data, _ := a.File(p)
	want := ts + "line one\n" + ts + "-----BEGIN RSA PRIVATE KEY-----\n" + ts + "[redacted]\n[kgwctl: truncated: sealed]\n"
	if string(data) != want {
		t.Errorf("log = %q, want %q", data, want)
	}
	if it := manifestItem(t, r.m, p); it.Status != pb.ItemOK || it.Source.Identity != pb.IdentityRereadFailed {
		t.Errorf("item %+v, want ok and reread-failed", it)
	}
	rb, _ := a.File(pb.RedactionsFile)
	if !strings.Contains(string(rb), `"rule":"pem-tail"`) {
		t.Errorf("redactions.json lacks pem-tail:\n%s", rb)
	}
	if c := r.m.Collectors; len(c) != 1 || c[0].Status != pb.CollectorTimeout {
		t.Errorf("collectors %+v, want logs timeout", c)
	}
	bundletest.AssertNoCanary(t, a, []string{canary})
}
