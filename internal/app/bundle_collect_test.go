package app_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

const bundleCanaries = "testdata/bundle/canaries.yaml"

// gwDir and kgwDir are the resource directories of the Gateway API and kgateway groups.
const (
	gwDir  = "resources/gateway.networking.k8s.io/"
	kgwDir = "resources/gateway.kgateway.dev/"
)

// ownershipFixture writes the cluster state of TestClusterStateOwnership and
// returns its path: a kgateway install whose discovery selector leaves out
// team-b, a second controller (example.com/other) with its GatewayClass,
// Gateway and HTTPRoute in other-only, a kept route in team-c, and
// Deployments in team-a (shared with a kgateway Gateway), team-c, other-only
// and team-b.
func ownershipFixture(t *testing.T) string {
	t.Helper()
	const fixture = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: kgateway
  namespace: kgateway-system
  labels: {kgateway: kgateway, app.kubernetes.io/name: kgateway, app.kubernetes.io/component: controller}
spec:
  selector: {matchLabels: {kgateway: kgateway}}
  template:
    metadata:
      labels: {kgateway: kgateway, app.kubernetes.io/name: kgateway, app.kubernetes.io/component: controller}
    spec:
      containers:
        - name: kgateway
          image: cr.kgateway.dev/kgateway-dev/kgateway:v2.5.0
          env:
            - name: KGW_DISCOVERY_NAMESPACE_SELECTORS
              value: '[{"matchExpressions":[{"key":"kubernetes.io/metadata.name","operator":"NotIn","values":["team-b"]}]}]'
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: kgateway}
spec: {controllerName: kgateway.dev/kgateway}
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: other}
spec: {controllerName: example.com/other}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: gw, namespace: team-a}
spec:
  gatewayClassName: kgateway
  listeners: [{name: http, protocol: HTTP, port: 8080, allowedRoutes: {namespaces: {from: All}}}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-a, namespace: team-a}
spec: {parentRefs: [{name: gw}]}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-b, namespace: team-b}
spec: {parentRefs: [{name: gw, namespace: team-a}]}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: route-c, namespace: team-c}
spec: {parentRefs: [{name: gw, namespace: team-a}]}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: edge, namespace: other-only}
spec:
  gatewayClassName: other
  listeners: [{name: http, protocol: HTTP, port: 8080}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: edge-route, namespace: other-only}
spec: {parentRefs: [{name: edge}]}
`
	deployment := func(ns, name string) string {
		return "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: " + name + ", namespace: " + ns + "}\n" +
			"spec:\n  selector: {matchLabels: {app: " + name + "}}\n  template:\n    metadata: {labels: {app: " + name + "}}\n" +
			"    spec: {containers: [{name: app, image: example.com/" + name + ":v1}]}\n"
	}
	body := strings.Join([]string{fixture, deployment("team-a", "other-app"), deployment("team-c", "app-c"), deployment("other-only", "other-app"), deployment("team-b", "app-b")}, "---\n")
	path := filepath.Join(t.TempDir(), "ownership.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// bundleArchive runs bundle to stdout and extracts the archive.
func bundleArchive(t *testing.T, env *testenv.Env, args ...string) bundletest.Archive {
	t.Helper()
	r := runBundle(t, env.Kubeconfig, nil, append([]string{"-o", "-"}, args...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	return bundletest.Extract(t, strings.NewReader(r.stdout))
}

// assertMembers fails for each of present that a lacks and each of absent it has.
func assertMembers(t *testing.T, a bundletest.Archive, present, absent []string) {
	t.Helper()
	for _, p := range present {
		if _, ok := a.File(p); !ok {
			t.Errorf("archive lacks %s", p)
		}
	}
	for _, p := range absent {
		if _, ok := a.File(p); ok {
			t.Errorf("archive has %s", p)
		}
	}
}

// membersUnder returns the member names under prefix, relative to the root.
func membersUnder(a bundletest.Archive, prefix string) []string {
	var out []string
	for _, m := range a.Members {
		if n := strings.TrimPrefix(m.Name, a.Root+"/"); strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// fileHas fails unless member path contains each of subs.
func fileHas(t *testing.T, a bundletest.Archive, path string, subs ...string) {
	t.Helper()
	b, ok := a.File(path)
	if !ok {
		t.Errorf("archive lacks %s", path)
		return
	}
	for _, s := range subs {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("%s lacks %q", path, s)
		}
	}
}

// TestClusterStateArchive: the resources, workloads and install collectors
// write design §4 paths, and canaries in the cluster never reach the archive.
func TestClusterStateArchive(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, bundleCanaries}})
	a := bundleArchive(t, env)
	assertMembers(t, a, []string{
		gwDir + "GatewayClass/_cluster/kgateway.yaml",
		gwDir + "Gateway/kgateway-system/http.yaml",
		gwDir + "HTTPRoute/default/example.yaml",
		kgwDir + "TrafficPolicy/default/tenant-headers.yaml",
		kgwDir + "GatewayExtension/kgateway-system/ext-auth.yaml",
		"workloads/kgateway-system/deployments.yaml",
		"workloads/kgateway-system/pods.yaml",
		"workloads/kgateway-system/services.yaml",
		"workloads/kgateway-system/events.yaml",
		"install/kgateway-system/kgateway.yaml",
		"install/_cluster/kgateway.yaml",
	}, nil)
	for _, m := range membersUnder(a, "") {
		if strings.Contains(m, "/secrets") || strings.Contains(m, "/configmaps") || strings.Contains(m, "/Secret/") || strings.Contains(m, "/ConfigMap/") {
			t.Errorf("member %s", m)
		}
	}
	fileHas(t, a, kgwDir+"TrafficPolicy/default/tenant-headers.yaml", "x-tenant-key", "[redacted]")
	fileHas(t, a, kgwDir+"GatewayExtension/kgateway-system/ext-auth.yaml", "x-tenant")
	fileHas(t, a, "workloads/kgateway-system/events.yaml", "kind: List", "Bearer [redacted]")
	fileHas(t, a, "install/kgateway-system/kgateway.yaml", "cr.kgateway.dev/kgateway-dev/kgateway:v2.5.0")
	bundletest.AssertNoCanary(t, a, []string{"kgwctl-canary-last-applied", "kgwctl-canary-tenant-key",
		"kgwctl-canary-headers-to-add", "kgwctl-canary-event-message"})
	for _, req := range env.Proxies[0].Requests() {
		if strings.Contains(req, "/secrets") || strings.Contains(req, "/configmaps") {
			t.Errorf("request sent: %s", req)
		}
	}
}

// TestClusterStateScope: -n team-a, also with --all-controllers, writes no
// member outside team-a and the install namespace, though team-c's route is
// kept by ownership and scope; GatewayClasses stay under _cluster.
func TestClusterStateScope(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{ownershipFixture(t)}})
	outside := func(t *testing.T, a bundletest.Archive) {
		t.Helper()
		for _, m := range membersUnder(a, "") {
			if strings.Contains(m, "team-b") || strings.Contains(m, "team-c") || strings.Contains(m, "other-only") {
				t.Errorf("member %s is outside -n team-a", m)
			}
		}
	}

	a := bundleArchive(t, env, "-n", "team-a")
	outside(t, a)
	assertMembers(t, a, []string{gwDir + "GatewayClass/_cluster/kgateway.yaml", gwDir + "HTTPRoute/team-a/route-a.yaml",
		"workloads/team-a/deployments.yaml", "install/_cluster/kgateway.yaml"}, []string{gwDir + "GatewayClass/_cluster/other.yaml"})
	// team-b's route is out of the bound, so it is never listed nor counted.
	if ex := excludedCounts(t, a); ex["gateway.networking.k8s.io/HTTPRoute scope"] != 0 {
		t.Errorf("excluded %v: team-b's route was listed", ex)
	}
	for _, req := range env.Proxies[0].Requests() {
		for _, ns := range []string{"team-b", "team-c", "other-only"} {
			if strings.Contains(req, "/namespaces/"+ns+"/") {
				t.Errorf("request outside the -n bound: %s", req)
			}
		}
	}

	// --all-controllers lifts ownership, never the -n bound; GatewayClasses are cluster-scoped.
	a = bundleArchive(t, env, "--all-controllers", "-n", "team-a")
	outside(t, a)
	assertMembers(t, a, []string{gwDir + "GatewayClass/_cluster/other.yaml", "install/_cluster/other.yaml",
		gwDir + "GatewayClass/_cluster/kgateway.yaml", "workloads/team-a/deployments.yaml"}, nil)
}

// excludedCounts returns the manifest's exclusions as "group/Kind reason" counts.
func excludedCounts(t *testing.T, a bundletest.Archive) map[string]int {
	t.Helper()
	var doc bundleScopeDoc
	mf, _ := a.File("manifest.json")
	if err := json.Unmarshal(mf, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range doc.Item.Excluded {
		out[e.Kind+" "+e.Reason] = e.Count
	}
	return out
}

// bundleScopeDoc is the part of manifest.json the ownership test reads.
type bundleScopeDoc struct {
	Item struct {
		Scope struct {
			Installs []struct {
				Discovery struct {
					Mode      string   `json:"mode"`
					Unwatched []string `json:"unwatched"`
				} `json:"discovery"`
			} `json:"installs"`
		} `json:"scope"`
		Excluded []struct {
			Kind, Reason string
			Count        int
		} `json:"excluded"`
	} `json:"item"`
}

// TestClusterStateOwnership: another controller's objects, and workloads in
// namespaces only it uses, are left out; a shared namespace keeps every
// Deployment; a selector excluding team-b leaves out team-b and records it;
// --all-controllers keeps everything.
func TestClusterStateOwnership(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{ownershipFixture(t)}})
	foreign := []string{
		gwDir + "GatewayClass/_cluster/other.yaml",
		gwDir + "Gateway/other-only/edge.yaml",
		gwDir + "HTTPRoute/other-only/edge-route.yaml",
		"install/_cluster/other.yaml",
		"workloads/other-only/deployments.yaml",
		gwDir + "HTTPRoute/team-b/route-b.yaml",
		"workloads/team-b/deployments.yaml",
	}
	ours := []string{
		gwDir + "GatewayClass/_cluster/kgateway.yaml",
		gwDir + "Gateway/team-a/gw.yaml",
		gwDir + "HTTPRoute/team-a/route-a.yaml",
		gwDir + "HTTPRoute/team-c/route-c.yaml",
		"workloads/team-c/deployments.yaml",
		"install/_cluster/kgateway.yaml",
		"install/kgateway-system/kgateway.yaml",
	}

	a := bundleArchive(t, env)
	assertMembers(t, a, ours, foreign)
	if m := membersUnder(a, "workloads/other-only/"); len(m) != 0 {
		t.Errorf("workloads from a namespace only the other controller uses: %v", m)
	}
	fileHas(t, a, "workloads/team-a/deployments.yaml", "name: other-app")
	mf, _ := a.File("manifest.json")
	var doc bundleScopeDoc
	if err := json.Unmarshal(mf, &doc); err != nil {
		t.Fatal(err)
	}
	if ins := doc.Item.Scope.Installs; len(ins) != 1 || ins[0].Discovery.Mode != "selectors" || !slices.Contains(ins[0].Discovery.Unwatched, "team-b") {
		t.Errorf("manifest scope %+v, want selectors with team-b unwatched", doc.Item.Scope)
	}
	excluded := excludedCounts(t, a)
	want := map[string]int{
		"gateway.networking.k8s.io/HTTPRoute scope":        1,
		"gateway.networking.k8s.io/HTTPRoute ownership":    1,
		"gateway.networking.k8s.io/Gateway ownership":      1,
		"gateway.networking.k8s.io/GatewayClass ownership": 1,
	}
	for k, n := range want {
		if excluded[k] != n {
			t.Errorf("excluded %q = %d, want %d (all: %v)", k, excluded[k], n, excluded)
		}
	}

	a = bundleArchive(t, env, "--all-controllers")
	assertMembers(t, a, append(ours, foreign...), nil)
	fileHas(t, a, "workloads/other-only/deployments.yaml", "name: other-app")
}

// TestBundleSkipLogs: --skip logs writes no logs/ member and records the
// collector skipped with reason flag, with no errors.txt line and no
// pods/log request; without it, the same cluster gets log requests.
func TestBundleSkipLogs(t *testing.T) {
	env := newEnv(t, testenv.Options{Fixtures: []string{healthyFixture, proxyPods(t, "http-a")}})
	isLog := func(req string) bool { p, _, _ := strings.Cut(req, "?"); return strings.HasSuffix(p, "/log") }

	bundleArchive(t, env)
	if !slices.ContainsFunc(env.Proxies[0].Requests(), isLog) {
		t.Fatal("no pods/log request without --skip logs; the check below proves nothing")
	}
	before := len(env.Proxies[0].Requests())

	a := bundleArchive(t, env, "--skip", "logs")
	for _, m := range membersUnder(a, "") {
		if strings.Contains(m, "/logs/") {
			t.Errorf("member %s", m)
		}
	}
	var found bool
	for _, c := range readManifest(t, a).Item.Collectors {
		if c.Name == "logs" {
			found = true
			if c.Status != "skipped" || c.Reason != "flag" {
				t.Errorf("logs collector %s (%s), want skipped (flag)", c.Status, c.Reason)
			}
		}
	}
	if !found {
		t.Error("no logs collector in the manifest")
	}
	if b, _ := a.File("errors.txt"); strings.Contains(string(b), "log") {
		t.Errorf("errors.txt:\n%s", b)
	}
	for _, req := range env.Proxies[0].Requests()[before:] {
		if isLog(req) {
			t.Errorf("request with --skip logs: %s", req)
		}
	}
}
