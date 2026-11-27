package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/proxystats"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const (
	checkInstall        = "testdata/check/install.yaml"
	checkController     = "testdata/check/controller.yaml"
	checkControllerDown = "testdata/check/controller-down.yaml"
	checkRouteBroken    = "testdata/check/route-broken.yaml"
)

// managed is the resources_managed series every owned Gateway needs, else the xDS check cannot judge.
const managed = `# TYPE kgateway_resources_managed gauge
kgateway_resources_managed{namespace="kgateway-system",parent="http",resource="Gateway"} 1
`

// metricsScraper serves a controller's /metrics: body, or managed alone.
type metricsScraper struct{ body string }

func (s metricsScraper) Scrape(context.Context, *corev1.Pod, int32) (io.ReadCloser, error) {
	if s.body == "" {
		s.body = managed
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}

// rejecting reports active xDS rejects on kgateway-system/http.
var rejecting = metricsScraper{body: managed + `# TYPE kgateway_envoy_xds_rejects_active gauge
kgateway_envoy_xds_rejects_active{gateway_name="http",gateway_namespace="kgateway-system",type_url="envoy.config.listener.v3.Listener"} 1
`}

// hangScraper blocks until the scrape's context ends.
type hangScraper struct{}

func (hangScraper) Scrape(ctx context.Context, _ *corev1.Pod, _ int32) (io.ReadCloser, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// fakeStats is a proxy stats source with one live proxy per Gateway.
type fakeStats struct{ disconnected bool }

func (fakeStats) Targets(_ install.Resolved, gw objects.Ref, _ *cache.Snapshot) ([]proxystats.Target, bool) {
	return []proxystats.Target{{ID: "uid-" + gw.Name, Name: gw.Name + "-6f7c9d8b4-ax0", Gateway: gw, Addr: "fake"}}, true
}

func (fakeStats) Fetch(context.Context, proxystats.Target) ([]byte, error) {
	return []byte("fake"), nil
}

func (s fakeStats) Interpret(t proxystats.Target, _ []byte) (proxystats.PodStats, error) {
	connected, zero := !s.disconnected, int64(0)
	return proxystats.PodStats{UID: t.ID, Name: t.Name, Gateway: t.Gateway, Connected: &connected,
		WarmingListeners: &zero, WarmingClusters: &zero, ServerState: "live"}, nil
}

var crdsGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// checkEnv is newEnv with one Gateway API bundle: the shared envtest mixes the
// standard and experimental channels (testenv README), which is KGWUI-022 in
// every report. It labels the experimental CRDs as the standard bundle and restores them at cleanup.
func checkEnv(t *testing.T, fixtures ...string) *testenv.Env {
	t.Helper()
	env := newEnv(t, testenv.Options{Fixtures: fixtures})
	dc, err := dynamic.NewForConfig(env.Admin)
	if err != nil {
		t.Fatal(err)
	}
	crds := dc.Resource(crdsGVR)
	list, err := crds.List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const version, channel = "gateway.networking.k8s.io/bundle-version", "gateway.networking.k8s.io/channel"
	for _, crd := range list.Items {
		a := crd.GetAnnotations()
		if !strings.HasSuffix(crd.GetName(), ".gateway.networking.x-k8s.io") || a[channel] == "standard" && a[version] == "v1.7.0" {
			continue
		}
		patch := func(v, c string) {
			body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{version: v, channel: c}}})
			if _, err := crds.Patch(context.Background(), crd.GetName(), types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
				t.Errorf("label CRD %s: %v", crd.GetName(), err)
			}
		}
		patch("v1.7.0", "standard")
		t.Cleanup(func() { patch(a[version], a[channel]) })
	}
	return env
}

// fastHooks keep each run short: a quiet period of 200ms and a first-publish wait of 1s.
func fastHooks(s ctrlmetrics.Scraper) app.CheckTestHooks {
	return app.CheckTestHooks{Scraper: s, Quiet: 200 * time.Millisecond, FirstPublishWait: time.Second}
}

// statsRegistry is the default registry with src as its proxy stats source.
func statsRegistry(src proxystats.Source) *extension.Registry {
	return extension.New(extension.WithProxyStatsSource(src))
}

// runCheck runs the root command built with reg and h against env's kubeconfig.
func runCheck(t *testing.T, env *testenv.Env, reg *extension.Registry, h app.CheckTestHooks, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	root := app.NewRootWithCheckHooks(app.Config{Registry: reg, Build: testBuild, Out: &out, Err: &errb, Now: testNow}, h)
	start := time.Now()
	code := run(root, append([]string{"--kubeconfig", env.Kubeconfig}, args...)...)
	return result{code: code, stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
}

// Times vary per run; the golden files hold placeholders.
var (
	stampRe = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`)
	clockRe = regexp.MustCompile(`\b\d\d:\d\d:\d\d\b`)
	inRe    = regexp.MustCompile(`\(in [0-9hms]+\)`)
)

// normalizeTimes replaces the run's times in text output with fixed placeholders.
func normalizeTimes(s string) string {
	s = stampRe.ReplaceAllString(s, "YYYY-MM-DD hh:mm:ss")
	s = clockRe.ReplaceAllString(s, "hh:mm:ss")
	return inRe.ReplaceAllString(s, "(in …)")
}

// stepClock advances fc by step every millisecond until the test ends, so
// fake-clock timers fire while the command runs.
func stepClock(t *testing.T, fc *clocktesting.FakeClock, step time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for ctx.Err() == nil {
			fc.Step(step)
			time.Sleep(time.Millisecond)
		}
	})
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func TestCheckThemes(t *testing.T) {
	cases := []struct {
		name     string
		fixtures []string
		reg      *extension.Registry
		hooks    func(t *testing.T) app.CheckTestHooks
		want     int
		// contains are lines the text output must have, beyond the golden file.
		contains []string
	}{
		{name: "healthy", fixtures: []string{checkInstall, checkController}, want: 0, contains: []string{"CHECKS RUN: "}},
		{name: "controller-down", fixtures: []string{checkInstall, checkControllerDown}, want: 1, contains: []string{"KGWUI-005"}},
		{name: "route-broken", fixtures: []string{checkInstall, checkController, checkRouteBroken}, want: 1,
			contains: []string{"Likely cause: Service shop/cart-v2", "KGWUI-013  HTTPRoute shop/cart"}},
		{name: "xds-rejected", fixtures: []string{checkInstall, checkController}, want: 1, contains: []string{"KGWUI-001  Gateway kgateway-system/http"},
			hooks: func(*testing.T) app.CheckTestHooks { return fastHooks(rejecting) }},
		// 026 needs the proxy disconnected for 60s: a stepped fake clock and a quiet period past that age.
		{name: "proxy-disconnected", fixtures: []string{checkInstall, checkController}, want: 1, contains: []string{"KGWUI-026  Gateway kgateway-system/http"},
			reg: statsRegistry(fakeStats{disconnected: true}),
			hooks: func(t *testing.T) app.CheckTestHooks {
				fc := clocktesting.NewFakeClock(time.Now())
				stepClock(t, fc, time.Second)
				h := fastHooks(metricsScraper{})
				h.Clock, h.Quiet, h.MetricsInterval = fc, 70*time.Second, 5*time.Minute
				return h
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := checkEnv(t, tc.fixtures...)
			reg := tc.reg
			if reg == nil {
				reg = statsRegistry(fakeStats{})
			}
			h := fastHooks(metricsScraper{})
			if tc.hooks != nil {
				h = tc.hooks(t)
			}
			r := runCheck(t, env, reg, h, "check")
			if r.code != tc.want {
				t.Errorf("exit %d, want %d; stderr: %s\nstdout:\n%s", r.code, tc.want, r.stderr, r.stdout)
			}
			if r.stderr != "" {
				t.Errorf("stderr = %q, want empty", r.stderr)
			}
			if strings.Contains(r.stdout, "\x1b") {
				t.Error("stdout is not a terminal but has ANSI escapes")
			}
			for _, c := range tc.contains {
				if !strings.Contains(r.stdout, c) {
					t.Errorf("stdout lacks %q:\n%s", c, r.stdout)
				}
			}
			golden(t, "check", tc.name, normalizeTimes(r.stdout))
		})
	}
}

func TestCheckRouteBrokenGroup(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check", "-o", "json")
	var doc struct {
		Item struct {
			Installs []struct {
				Findings []struct{ ID, Code, Group string }
				Groups   []struct {
					ID       string
					Cause    *struct{ Kind, Namespace, Name string }
					Findings []string
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || len(doc.Item.Installs) != 1 {
		t.Fatalf("decode: %v (installs %d)\n%s", err, len(doc.Item.Installs), r.stdout)
	}
	in := doc.Item.Installs[0]
	var group string
	for _, f := range in.Findings {
		if f.Code == "KGWUI-013" {
			group = f.Group
		}
	}
	for _, g := range in.Groups {
		if g.ID == group {
			if g.Cause == nil || g.Cause.Kind != "Service" || g.Cause.Namespace != "shop" || g.Cause.Name != "cart-v2" {
				t.Errorf("013's group cause = %+v, want Service shop/cart-v2", g.Cause)
			}
			return
		}
	}
	t.Fatalf("013 in no diagnosis group (group %q)\n%s", group, r.stdout)
}

// assertReport fails unless stdout is the report in format: text with its header,
// or exactly one Check envelope with installs installs; contains must appear in it.
func assertReport(t *testing.T, format, stdout string, installs int, contains ...string) {
	t.Helper()
	switch format {
	case "text":
		if !strings.HasPrefix(stdout, "kgwctl check · ") {
			t.Errorf("text: no report header:\n%s", stdout)
		}
	case "yaml":
		j, err := yaml.YAMLToJSON([]byte(stdout))
		if err != nil {
			t.Fatalf("yaml: %v", err)
		}
		oneEnvelope(t, j, installs)
	default:
		oneEnvelope(t, []byte(stdout), installs)
	}
	for _, c := range contains {
		if !strings.Contains(stdout, c) {
			t.Errorf("%s: report lacks %q:\n%s", format, c, stdout)
		}
	}
}

// TestCheckExit2WithReport: exit 2 wins over findings, and the report is still printed in every format.
func TestCheckExit2WithReport(t *testing.T) {
	const noInstall = "Error: no kgateway install found; run \"kgwctl version\" to see what the cluster runs\n"
	cases := []struct {
		name     string
		fixtures []string
		args     []string
		deny     bool
		installs int
		stderr   string
		contains []string
	}{
		{name: "no-install", installs: 0, stderr: noInstall},
		{name: "install-not-matched", fixtures: []string{checkInstall, checkController}, args: []string{"--install", "nope/nope"}, installs: 0, stderr: noInstall},
		// 005 alone would exit 1; unreadable HTTPRoutes make it 2.
		{name: "error-and-core-denied", fixtures: []string{checkInstall, checkControllerDown}, deny: true, installs: 1,
			contains: []string{"KGWUI-005", "httproutes.gateway.networking.k8s.io"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := checkEnv(t, tc.fixtures...)
			if tc.deny {
				env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden})
			}
			for _, format := range []string{"text", "json", "yaml"} {
				args := append([]string{"check", "-o", format}, tc.args...)
				r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), args...)
				if r.code != 2 {
					t.Errorf("%s: exit %d, want 2; stderr: %s", format, r.code, r.stderr)
				}
				if tc.stderr != "" && r.stderr != tc.stderr {
					t.Errorf("%s: stderr = %q, want %q", format, r.stderr, tc.stderr)
				}
				if !strings.HasPrefix(r.stderr, "Error: ") {
					t.Errorf("%s: stderr = %q, want an Error line", format, r.stderr)
				}
				assertReport(t, format, r.stdout, tc.installs, tc.contains...)
			}
		})
	}
}

// findingCodes returns the codes of the error and warning findings in a JSON check document.
func findingCodes(t *testing.T, stdout string) []string {
	t.Helper()
	var doc struct {
		Item struct {
			Installs []struct {
				Findings []struct{ Code, Severity string }
			}
		}
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	var out []string
	for _, in := range doc.Item.Installs {
		for _, f := range in.Findings {
			if f.Severity != "info" {
				out = append(out, f.Code)
			}
		}
	}
	return out
}

func TestCheckPodsProxyDenied(t *testing.T) {
	fixtures := []string{checkInstall, checkController, checkRouteBroken}
	env := checkEnv(t, fixtures...)
	base := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check", "-o", "json")

	env.Proxies[0].Rule(testenv.Match{Resource: "pods", Subresource: "proxy"}, testenv.Action{Status: http.StatusForbidden})
	// The production scraper reads the controller metrics through pods/proxy.
	h := fastHooks(nil)
	denied := runCheck(t, env, statsRegistry(fakeStats{}), h, "check", "-o", "json")
	if denied.code != base.code {
		t.Errorf("exit %d, want the findings' exit %d; stderr: %s", denied.code, base.code, denied.stderr)
	}
	if got, want := strings.Join(findingCodes(t, denied.stdout), ","), strings.Join(findingCodes(t, base.stdout), ","); got != want {
		t.Errorf("findings = %s, want %s as with metrics", got, want)
	}
	text := runCheck(t, env, statsRegistry(fakeStats{}), h, "check")
	_, notChecked, _ := strings.Cut(text.stdout, "NOT CHECKED")
	notChecked, _, _ = strings.Cut(notChecked, "CHECKS RUN")
	for _, name := range checksReadingMetrics(statsRegistry(fakeStats{})) {
		if !strings.Contains(notChecked, name) {
			t.Errorf("check %q not under NOT CHECKED:\n%s", name, text.stdout)
		}
	}
	if !strings.Contains(notChecked, "docs/rbac.yaml") {
		t.Errorf("NOT CHECKED gives no RBAC pointer:\n%s", text.stdout)
	}
}

// checksReadingMetrics returns the names of reg's checks that read the controller metrics.
func checksReadingMetrics(reg *extension.Registry) []string {
	var out []string
	for _, c := range reg.Checks {
		for _, k := range c.Inputs().Collected {
			if strings.HasPrefix(k, ctrlmetrics.CollectorName+"/") {
				out = append(out, c.Name())
				break
			}
		}
	}
	return out
}

func TestCheckCoreDenied(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindAcquire, Resource: httpRoutes}, testenv.Action{Status: http.StatusForbidden})
	r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check")
	if r.code != 2 {
		t.Errorf("exit %d, want 2; stderr: %s", r.code, r.stderr)
	}
	for _, s := range []string{"Install kgateway-system/kgateway", "No access to httproutes.gateway.networking.k8s.io"} {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("report lacks %q:\n%s", s, r.stdout)
		}
	}
	if !strings.HasPrefix(r.stderr, "Error: ") {
		t.Errorf("stderr = %q, want an Error line", r.stderr)
	}
}

// oneEnvelope fails unless s decodes as exactly one JSON value, a Check envelope with installs installs.
func oneEnvelope(t *testing.T, s []byte, installs int) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(s))
	var doc struct {
		SchemaVersion, Kind string
		Item                struct{ Installs []json.RawMessage }
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode: %v\n%s", err, s)
	}
	if doc.Kind != "Check" || doc.SchemaVersion != print.SchemaVersion || len(doc.Item.Installs) != installs {
		t.Errorf("envelope = %s %s with %d installs, want Check %s with %d", doc.Kind, doc.SchemaVersion, len(doc.Item.Installs), print.SchemaVersion, installs)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Errorf("a second document follows the envelope: %v", err)
	}
}

func TestCheckJSONOneDocument(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check", "-o", "json")
	if r.code != 1 {
		t.Errorf("exit %d, want 1; stderr: %s", r.code, r.stderr)
	}
	oneEnvelope(t, []byte(r.stdout), 1)
}

func TestCheckYAMLOneDocument(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check", "-o", "yaml")
	if r.code != 1 {
		t.Errorf("exit %d, want 1; stderr: %s", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "\n---") || strings.HasPrefix(r.stdout, "---") {
		t.Errorf("YAML output has a document separator:\n%s", r.stdout)
	}
	j, err := yaml.YAMLToJSON([]byte(r.stdout))
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	oneEnvelope(t, j, 1)
}

func TestCheckNamespaceFilter(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController, checkRouteBroken)
	reg := statsRegistry(fakeStats{})
	other := runCheck(t, env, reg, fastHooks(metricsScraper{}), "check", "-n", "default")
	if other.code != 0 || strings.Contains(other.stdout, "KGWUI-013") {
		t.Errorf("-n default: exit %d, want 0 without shop's 013:\n%s", other.code, other.stdout)
	}
	shop := runCheck(t, env, reg, fastHooks(metricsScraper{}), "check", "-n", "shop")
	if shop.code != 1 || !strings.Contains(shop.stdout, "KGWUI-013  HTTPRoute shop/cart") {
		t.Errorf("-n shop: exit %d, want 1 with 013:\n%s", shop.code, shop.stdout)
	}
}

func TestCheckDeadline(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	h := fastHooks(hangScraper{})
	h.FirstPublishWait = time.Hour // only the deadline ends this run
	r := runCheck(t, env, statsRegistry(fakeStats{}), h, "check", "--request-timeout", "2s")
	if r.took < 2*time.Second || r.took >= 4*time.Second {
		t.Errorf("returned after %v, want between 2s and 4s", r.took)
	}
	_, notChecked, _ := strings.Cut(r.stdout, "NOT CHECKED")
	if !strings.Contains(notChecked, "not collected yet") {
		t.Errorf("the metrics checks are not under NOT CHECKED as not collected:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, ctrlmetrics.CollectorName+": loading") {
		t.Errorf("the hanging collector is not an incomplete input:\n%s", r.stdout)
	}
	if r.code != 0 {
		t.Errorf("exit %d, want the findings' exit 0; stderr: %s", r.code, r.stderr)
	}
}

func TestCheckDiscoveryTimeout(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Delay: time.Hour})
	r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(metricsScraper{}), "check", "--request-timeout", "1s")
	if r.code != 2 {
		t.Errorf("exit %d, want 2; stderr: %s", r.code, r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	if r.took > 2*time.Second {
		t.Errorf("returned after %v, want within 2s", r.took)
	}
}

func TestCheckNoAccumulation(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	reg := statsRegistry(fakeStats{})
	first, firstConns := 0, 0
	for i := range 10 {
		if r := runCheck(t, env, reg, fastHooks(metricsScraper{}), "check"); r.code != 0 {
			t.Fatalf("run %d: exit %d, stderr: %s", i, r.code, r.stderr)
		}
		if i == 0 {
			first = runtime.NumGoroutine()
			firstConns = settledConns(env.Proxies[0])
		}
	}
	waitNoWatches(t, env.Proxies[0])
	if n := settledConns(env.Proxies[0]); n > firstConns {
		t.Errorf("open connections after 10 runs = %d, after 1 run = %d", n, firstConns)
	}
	// A compute in flight at Stop finishes on its own (design §4.3): poll with a bound.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > first+5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > first+5 {
		t.Errorf("goroutines after 10 runs = %d, after 1 run = %d", n, first)
	}
}

func TestExecuteExitError(t *testing.T) {
	cases := []struct {
		err        error
		code       int
		wantStderr string
	}{
		{err: &app.ExitError{Code: 2}, code: 2},
		{err: &app.ExitError{Code: 2, Err: errors.New("no kgateway install found")}, code: 2, wantStderr: "Error: no kgateway install found\n"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		cmd := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return tc.err }}
		root := app.NewRoot(app.Config{Commands: []*cobra.Command{cmd}, Out: &out, Err: &errb})
		if code := run(root, "probe"); code != tc.code || errb.String() != tc.wantStderr {
			t.Errorf("%v: exit %d stderr %q, want %d %q", tc.err, code, errb.String(), tc.code, tc.wantStderr)
		}
	}
}

// TestCheckInterrupted cancels the command context, as Ctrl-C does, once a
// model with the install exists: exit 2 with the report so far.
func TestCheckInterrupted(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := fastHooks(hangScraper{})
	h.FirstPublishWait = time.Hour // only the cancel ends this run
	h.Waiting = func(p *engine.Published) {
		if p != nil && p.Model.Snapshot != nil && p.Model.Snapshot.Synced && len(p.Model.Installs) > 0 {
			cancel()
		}
	}
	out, code := executeCheck(t, ctx, env, h)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	for _, s := range []string{"Install kgateway-system/kgateway", "controller-metrics: loading"} {
		if !strings.Contains(out, s) {
			t.Errorf("report lacks %q:\n%s", s, out)
		}
	}
	waitNoWatches(t, env.Proxies[0])
}

// TestCheckInterruptedInDiscovery cancels while discovery hangs: exit 2, nothing on stdout.
func TestCheckInterruptedInDiscovery(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	hits := env.Proxies[0].Rule(testenv.Match{Kind: testenv.KindDiscovery, Resource: "*"}, testenv.Action{Delay: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for hits.N() == 0 && ctx.Err() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	out, code := executeCheck(t, ctx, env, fastHooks(metricsScraper{}))
	if code != 2 || out != "" {
		t.Errorf("exit %d stdout %q, want 2 and empty", code, out)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("returned after %v, want soon after the cancel", took)
	}
}

// executeCheck runs check under ctx and returns stdout and the exit code Execute would give.
func executeCheck(t *testing.T, ctx context.Context, env *testenv.Env, h app.CheckTestHooks) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	root := app.NewRootWithCheckHooks(app.Config{Registry: statsRegistry(fakeStats{}), Build: testBuild, Out: &out, Err: &errb, Now: testNow}, h)
	root.SetArgs([]string{"--kubeconfig", env.Kubeconfig, "check", "--request-timeout", "0"})
	err := root.ExecuteContext(ctx)
	var ee *app.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.Code
	}
	t.Logf("error: %v", err)
	return out.String(), 1
}

// errScraper fails every scrape with err.
type errScraper struct{ err string }

func (s errScraper) Scrape(context.Context, *corev1.Pod, int32) (io.ReadCloser, error) {
	return nil, errors.New(s.err)
}

// TestCheckRedactsURLs: a scrape's transport error reaches the report without the server's address (design §3.3 rule 1).
func TestCheckRedactsURLs(t *testing.T) {
	env := checkEnv(t, checkInstall, checkController)
	u, err := url.Parse(env.Proxies[0].URL())
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	// want is short: YAML folds long lines and JSON escapes quotes.
	for _, tc := range []struct{ err, gone, want string }{
		// A URL: scheme kept, host, port and path gone.
		{`Get "https://10.0.0.1:6443/api/v1/namespaces/x/pods/y:9092/proxy/metrics": tls: failed to verify certificate: x509: certificate signed by unknown authority`,
			"10.0.0.1", `https://<redacted>`},
		// The API server's own host outside a URL: a DNS error and a certificate name mismatch.
		{`dial tcp: lookup ` + host + ` on 10.96.0.10:53: no such host`, host, `lookup <apiserver> on <redacted>:`},
		{`tls: failed to verify certificate: x509: certificate is valid for other, not ` + host, host, `not <apiserver>`},
	} {
		for _, out := range []string{"text", "json", "yaml"} {
			r := runCheck(t, env, statsRegistry(fakeStats{}), fastHooks(errScraper{tc.err}), "check", "-o", out)
			// JSON escapes < and >.
			r.stdout = strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(r.stdout)
			if strings.Contains(r.stdout, tc.gone) || strings.Contains(r.stdout, "/api/v1/namespaces/x") || strings.Contains(r.stdout, "10.96.0.10") {
				t.Errorf("%s: server address in the report:\n%s", out, r.stdout)
			}
			if !strings.Contains(r.stdout, tc.want) {
				t.Errorf("%s: report lacks %q:\n%s", out, tc.want, r.stdout)
			}
		}
	}
}
