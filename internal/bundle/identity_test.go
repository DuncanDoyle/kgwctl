package bundle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// The expected UID of Pod gw/p-0, and the UID of its replacement.
const uid1, uid2 = "0b8a3c1e-5f1d-4b7a-9c2e-1d2f3a4b5c6d", "7c1d2e3f-0a1b-4c2d-8e3f-4a5b6c7d8e9f"

// podSrc is the Source of a capture of Pod gw/p-0 with its expected UID.
var podSrc = pb.Source{Namespace: "gw", Pod: "p-0", UID: uid1, Container: "envoy", Operation: pb.OpLog}

// podClient returns a fake clientset holding gw/p-0 with uid1 and a counter
// of the Pod GETs sent to it.
func podClient() (*kubefake.Clientset, *atomic.Int64) {
	kube := kubefake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: "p-0", UID: uid1}})
	var gets atomic.Int64
	kube.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		return false, nil, nil // fall through to the tracker
	})
	return kube, &gets
}

// podHarness is a writer whose Pod re-reads go to kube.
func podHarness(t *testing.T, kube *kubefake.Clientset) *harness {
	t.Helper()
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	h.w.usePods(kube, time.Minute)
	return h
}

// committedItem finalizes h and returns the manifest item at p, read from the archive.
func committedItem(t *testing.T, h *harness, p string, order ...string) (Item, bundletest.Archive) {
	t.Helper()
	h.w.Seal()
	a := h.finish(t, Manifest{}, order...)
	it := itemByPath(archiveManifest(t, a).Items, p)
	if len(it) != 1 {
		t.Fatalf("manifest items at %s: %+v", p, it)
	}
	return it[0], a
}

func TestIdentityVerified(t *testing.T) {
	kube, gets := podClient()
	h := podHarness(t, kube)
	p := pb.Path{"proxies", "gw", "p-0", "server_info.json"}
	if err := h.w.Sink("c").JSON(bg, JSONServerInfo, p, strings.NewReader(`{"v":1}`), podSrc); err != nil {
		t.Fatal(err)
	}
	it, _ := committedItem(t, h, p.String(), "c")
	want := podSrc
	want.Identity = pb.IdentityVerified
	if it.Status != pb.ItemOK || it.Source != want {
		t.Errorf("item %+v, want ok with %+v", it, want)
	}
	if n := gets.Load(); n != 1 {
		t.Errorf("%d Pod GETs, want 1", n)
	}
}

// TestIdentityAfterStreamedRead: the Pod is replaced or deleted while a Text
// read is open; the re-read after the read ends reports it.
func TestIdentityAfterStreamedRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(ctx context.Context, kube *kubefake.Clientset) error
		want pb.CaptureIdentity
	}{
		{"changed", func(ctx context.Context, kube *kubefake.Clientset) error {
			if err := kube.CoreV1().Pods("gw").Delete(ctx, "p-0", metav1.DeleteOptions{}); err != nil {
				return err
			}
			_, err := kube.CoreV1().Pods("gw").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "gw", Name: "p-0", UID: uid2}}, metav1.CreateOptions{})
			return err
		}, pb.IdentityChanged},
		{"gone", func(ctx context.Context, kube *kubefake.Clientset) error {
			return kube.CoreV1().Pods("gw").Delete(ctx, "p-0", metav1.DeleteOptions{})
		}, pb.IdentityGone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube, gets := podClient()
			h := podHarness(t, kube)
			p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
			pr, pw := io.Pipe()
			done := make(chan error, 1)
			go func() { done <- h.w.Sink("logs").Text(bg, p, pr, podSrc) }()
			if _, err := pw.Write([]byte("first line\n")); err != nil {
				t.Fatal(err)
			}
			if n := gets.Load(); n != 0 {
				t.Fatalf("%d Pod GETs while the read is open, want 0", n)
			}
			if err := tc.swap(t.Context(), kube); err != nil {
				t.Fatal(err)
			}
			_, _ = pw.Write([]byte("last line\n"))
			pw.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			it, a := committedItem(t, h, p.String(), "logs")
			if it.Status != pb.ItemOK || it.Source.Identity != tc.want {
				t.Errorf("item %+v, want ok and identity %s", it, tc.want)
			}
			if data, _ := a.File(p.String()); string(data) != "first line\nlast line\n" {
				t.Errorf("log %q", data)
			}
		})
	}
}

// TestIdentityRereadFailed: a failed or slow re-read is metadata only; the
// item and its collector stay ok and errors.txt has no line for it.
func TestIdentityRereadFailed(t *testing.T) {
	forbidden := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
	}
	// Held until the request timeout cancels the request.
	slow := func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	failing := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	for name, fn := range map[string]http.HandlerFunc{"forbidden": forbidden, "timeout": slow, "error": failing} {
		t.Run(name, func(t *testing.T) {
			kube, rec := guardedClient(t, fn)
			h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
			p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
			c := fake("logs", func(ctx context.Context, s pb.Sink) error {
				return s.Text(ctx, p, strings.NewReader("a line\n"), podSrc)
			})
			env := runEnv()
			env.Kube, env.RequestTimeout = kube, 50*time.Millisecond
			m, err := Run(t.Context(), []pb.Collector{c}, env, h.w, Manifest{}, RunOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := rec.requests(); len(got) != 1 || got[0] != "GET /api/v1/namespaces/gw/pods/p-0" {
				t.Errorf("requests %v, want one Pod GET through the guard", got)
			}
			a := bundletest.Extract(t, bytes.NewReader(h.buf.Bytes()))
			it := itemByPath(archiveManifest(t, a).Items, p.String())
			if len(it) != 1 || it[0].Status != pb.ItemOK || it[0].Source.Identity != pb.IdentityRereadFailed {
				t.Errorf("item %+v, want ok with reread-failed", it)
			}
			if e := statusOf(t, m, "logs"); e.Status != pb.CollectorOK {
				t.Errorf("collector %+v, want ok", e)
			}
			if eb, _ := a.File(pb.ErrorsFile); len(eb) != 0 {
				t.Errorf("errors.txt:\n%s", eb)
			}
		})
	}
}

// cutReader serves body and records the sequence number of its last Read.
type cutReader struct {
	r    io.Reader
	seq  *atomic.Int64
	last atomic.Int64
}

func (c *cutReader) Read(p []byte) (int, error) {
	c.last.Store(c.seq.Add(1))
	return c.r.Read(p)
}

// TestIdentityAfterCut: a Text item cut at its cap is still re-read, after the cut.
func TestIdentityAfterCut(t *testing.T) {
	kube, _ := podClient()
	var seq, getAt atomic.Int64
	kube.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		getAt.Store(seq.Add(1))
		return false, nil, nil
	})
	h := podHarness(t, kube)
	p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
	r := &cutReader{r: bytes.NewReader(textBody("big", 11*1024)), seq: &seq}
	if err := h.w.Sink("logs").Text(bg, p, r, podSrc); err != nil {
		t.Fatal(err)
	}
	it, _ := committedItem(t, h, p.String(), "logs")
	if !strings.HasPrefix(it.Reason, "truncated") || it.Source.Identity != pb.IdentityVerified {
		t.Errorf("item %+v, want truncated and verified", it)
	}
	if getAt.Load() <= r.last.Load() {
		t.Errorf("Pod GET at %d, last read at %d: want the GET after the cut", getAt.Load(), r.last.Load())
	}
}

// TestIdentityInputsRejected: a Pod source without namespace or UID, or a
// caller-set identity, is rejected before anything is staged or re-read.
func TestIdentityInputsRejected(t *testing.T) {
	kube, gets := podClient()
	h := podHarness(t, kube)
	s := h.w.Sink("c")
	noNS, noUID, preset := podSrc, podSrc, podSrc
	noNS.Namespace, noUID.UID, preset.Identity = "", "", pb.IdentityVerified
	cases := map[string]pb.Source{"no-namespace": noNS, "no-uid": noUID, "preset": preset, "preset-no-pod": {Identity: pb.IdentityVerified}}
	for name, src := range cases {
		if err := s.Text(bg, pb.Path{"x", name + ".log"}, strings.NewReader("a\n"), src); !errors.Is(err, pb.ErrInvalidPath) {
			t.Errorf("%s: err = %v, want ErrInvalidPath", name, err)
		}
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	for name := range cases {
		if _, ok := a.File("x/" + name + ".log"); ok {
			t.Errorf("%s: member written", name)
		}
		if it := itemByPath(h.w.Items(), "x/"+name+".log"); len(it) != 1 || it[0].Status != pb.ItemFailed || it[0].Reason != ReasonInvalidSource {
			t.Errorf("%s: item %+v", name, it)
		}
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("%d Pod GETs, want 0", n)
	}
}

// TestIdentityNotReread: no Pod means no re-read; a write that already failed
// (parse error, staging drop) sends no GET.
func TestIdentityNotReread(t *testing.T) {
	kube, gets := podClient()
	l := mustLimits(t, 32<<20)
	sp, err := NewSpool(t.TempDir(), 1<<10, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := NewWriter(Stdout(&buf), sp, "root", l, redact.Options{}, "", fixedNow)
	w.usePods(kube, time.Minute)
	s := w.Sink("c")
	if err := s.JSON(bg, JSONServerInfo, pb.Path{"x", "plain.json"}, strings.NewReader(`{}`), pb.Source{Namespace: "gw"}); err != nil {
		t.Fatal(err)
	}
	if err := s.JSON(bg, JSONServerInfo, pb.Path{"x", "bad.json"}, strings.NewReader(`{`), podSrc); err == nil {
		t.Error("invalid JSON accepted")
	}
	if err := s.JSON(bg, JSONServerInfo, pb.Path{"x", "big.json"}, strings.NewReader(`"`+strings.Repeat("a", 8<<10)+`"`), podSrc); !errors.Is(err, pb.ErrBudget) {
		t.Errorf("structured staging drop: err = %v", err)
	}
	if err := s.Text(bg, pb.Path{"x", "big.log"}, bytes.NewReader(textBody("x", 16)), podSrc); !errors.Is(err, pb.ErrBudget) {
		t.Errorf("text staging drop: err = %v", err)
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("%d Pod GETs, want 0", n)
	}
	if it := itemByPath(w.Items(), "x/plain.json"); len(it) != 1 || it[0].Source.Identity != "" {
		t.Errorf("plain item %+v, want no identity", it)
	}
}

// blockingGet holds every Pod GET until release is closed; started is closed
// once the first GET is held.
func blockingGet(kube *kubefake.Clientset) (started, release chan struct{}) {
	started, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	kube.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		once.Do(func() { close(started) })
		<-release
		return false, nil, nil
	})
	return started, release
}

// TestIdentitySealDuringReread: the seal lands while a re-read GET is held.
// The pending result is discarded: the kept text item gets reread-failed, a
// structured item is not written, and both calls count as late.
func TestIdentitySealDuringReread(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprint("structured=", structured), func(t *testing.T) {
			kube, _ := podClient()
			started, release := blockingGet(kube)
			h := podHarness(t, kube)
			p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
			if structured {
				p = pb.Path{"proxies", "gw", "p-0", "server_info.json"}
			}
			done := make(chan error, 1)
			go func() {
				if structured {
					done <- h.w.Sink("c").JSON(bg, JSONServerInfo, p, strings.NewReader(`{"v":1}`), podSrc)
					return
				}
				done <- h.w.Sink("c").Text(bg, p, strings.NewReader("a line\n"), podSrc)
			}()
			<-started
			h.w.Seal()
			a := h.finish(t, Manifest{}, "c")
			close(release)
			if err := <-done; !errors.Is(err, pb.ErrSealed) {
				t.Errorf("write err = %v, want ErrSealed", err)
			}
			if n := h.w.Late()["c"]; n != 1 {
				t.Errorf("late = %d, want 1", n)
			}
			data, ok := a.File(p.String())
			it := itemByPath(archiveManifest(t, a).Items, p.String())
			if structured {
				if ok || len(it) != 0 {
					t.Errorf("late structured item written: %q %+v", data, it)
				}
				return
			}
			if string(data) != "a line\n" {
				t.Errorf("kept log %q", data)
			}
			if len(it) != 1 || it[0].Status != pb.ItemOK || it[0].Source.Identity != pb.IdentityRereadFailed {
				t.Errorf("kept item %+v, want ok with reread-failed", it)
			}
		})
	}
}

// TestIdentitySealMidStream: Pod text cut by the seal before its read ends
// is kept with reread-failed and sends no GET.
func TestIdentitySealMidStream(t *testing.T) {
	kube, gets := podClient()
	h := podHarness(t, kube)
	p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- h.w.Sink("logs").Text(bg, p, pr, podSrc) }()
	if _, err := pw.Write([]byte("line one\n")); err != nil {
		t.Fatal(err)
	}
	// Text handles the first line before it reads again.
	if _, err := pw.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "logs")
	pw.CloseWithError(errors.New("canceled"))
	if err := <-done; !errors.Is(err, pb.ErrSealed) {
		t.Errorf("Text err = %v", err)
	}
	it := itemByPath(archiveManifest(t, a).Items, p.String())
	if len(it) != 1 || it[0].Source.Identity != pb.IdentityRereadFailed {
		t.Errorf("kept item %+v, want reread-failed", it)
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("%d Pod GETs after the seal, want 0", n)
	}
}

// TestErrorDropsCallerIdentity: Error never publishes a caller-set identity,
// as Source.Identity is output only on every Sink method.
func TestErrorDropsCallerIdentity(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	src := podSrc
	src.Identity = pb.IdentityVerified
	p := pb.Path{"proxies", "gw", "p-0", "logs", "envoy.log"}
	h.w.Sink("logs").Error(p, src, errors.New("boom"))
	it, _ := committedItem(t, h, p.String(), "logs")
	if it.Source.Identity != "" || it.Source.UID != uid1 {
		t.Errorf("error item source %+v, want the source without its identity", it.Source)
	}
}

// TestNilContextIsBackground: a write with a nil ctx runs as with
// context.Background(), re-read included.
func TestNilContextIsBackground(t *testing.T) {
	kube, gets := podClient()
	h := podHarness(t, kube)
	var nilCtx context.Context
	p := pb.Path{"proxies", "gw", "p-0", "server_info.json"}
	if err := h.w.Sink("c").JSON(nilCtx, JSONServerInfo, p, strings.NewReader(`{}`), podSrc); err != nil {
		t.Fatal(err)
	}
	it, _ := committedItem(t, h, p.String(), "c")
	if it.Status != pb.ItemOK || it.Source.Identity != pb.IdentityVerified || gets.Load() != 1 {
		t.Errorf("item %+v after %d GETs, want ok and verified after 1", it, gets.Load())
	}
}
