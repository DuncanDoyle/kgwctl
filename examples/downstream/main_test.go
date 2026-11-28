package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/bundletest"
	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

// newEnv applies the vendor install and the extra fixtures.
func newEnv(t *testing.T, extra ...string) *testenv.Env {
	t.Helper()
	return testenv.New(t, testenv.Options{
		Fixtures: append([]string{"testdata/install.yaml"}, extra...),
		CRDDirs:  []string{"testdata/examplepolicies.crd.yaml"},
	})
}

// runRoot runs newRoot with args against env and returns the exit code and output.
func runRoot(t *testing.T, env *testenv.Env, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"--kubeconfig", env.Kubeconfig}, args...))
	code = cli.Execute(root)
	return code, out.String(), errb.String()
}

func TestDownstreamVersionShowsInstall(t *testing.T) {
	env := newEnv(t)
	code, stdout, stderr := runRoot(t, env, "version", "-o", "json")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	var got struct {
		Item struct {
			Client   struct{ Name string }
			Installs []struct{ ID, Display, Controller string }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.Item.Client.Name != "vendorctl" {
		t.Errorf("client name = %q, want vendorctl", got.Item.Client.Name)
	}
	if len(got.Item.Installs) != 1 || got.Item.Installs[0].Display != "vendor-system/vendor-controller" || got.Item.Installs[0].Controller != "1.2.3" {
		t.Errorf("installs = %+v, want the vendor controller at 1.2.3", got.Item.Installs)
	}
}

func TestDownstreamKindRegisteredAndServed(t *testing.T) {
	env := newEnv(t)
	reg := newRegistry()
	var found bool
	for _, d := range reg.Kinds {
		if d.Group == "policy.example.com" && d.Kind == "ExamplePolicy" && d.Category == kinds.CategoryPolicy {
			found = true
		}
	}
	if !found {
		t.Fatal("ExamplePolicy is not in the registry's kinds")
	}
	rc, err := clientcmd.BuildConfigFromFlags("", env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	c, err := load.NewClients(t.Context(), rc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := load.Load(t.Context(), c, reg, load.Needs{})
	if err != nil {
		t.Fatal(err)
	}
	var served bool
	for _, s := range res.Served {
		if s.Kind == "ExamplePolicy" && s.Version == "v1" {
			served = true
		}
	}
	if !served {
		t.Errorf("ExamplePolicy is not served: %+v", res.Served)
	}
}

func TestDownstreamPolicyInGet(t *testing.T) {
	env := newEnv(t)
	code, stdout, stderr := runRoot(t, env, "get", "examplepolicies", "-A")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	// A policy kind gets the policy columns; with no ancestors both cells say so.
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "NAMESPACE NAME ANCESTORS ACCEPTED AGE" {
		t.Fatalf("stdout = %q, want a header and one row", stdout)
	}
	row := strings.Fields(lines[1])
	if len(row) != 6 || row[0] != "default" || row[1] != "lonely" || row[2] != "<none>" || row[3]+" "+row[4] != "<no status>" {
		t.Errorf("row = %q, want default/lonely with no ancestors and <no status>", lines[1])
	}
}

func TestDownstreamCommand(t *testing.T) {
	env := newEnv(t)
	code, stdout, stderr := runRoot(t, env, "hello")
	if code != 0 || stdout != "hello\n" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and \"hello\\n\"", code, stdout, stderr)
	}
}

// TestDownstreamProxyRoutes: proxy routes runs with the wrapping module and
// matches the route as the default build does; --file needs no cluster.
func TestDownstreamProxyRoutes(t *testing.T) {
	var out, errb bytes.Buffer
	root := newRoot()
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"proxy", "routes", "--file", "../../testdata/dumps/collide-meta.json", "--route", "kgwctl-proxy/orders"})
	const want = "└─ route / (prefix) · HTTPRoute kgwctl-proxy/orders · rule 0 · match 0\n"
	if code := cli.Execute(root); code != 0 || errb.Len() != 0 || !strings.Contains(out.String(), want) || strings.Contains(out.String(), "no route matches") {
		t.Errorf("exit %d, stderr %q, stdout:\n%s\nwant the matched route %q", code, errb.String(), out.String(), want)
	}
}

// bundleArchive runs `bundle` against env and returns the extracted archive.
func bundleArchive(t *testing.T, env *testenv.Env, args ...string) bundletest.Archive {
	t.Helper()
	code, stdout, stderr := runRoot(t, env, append([]string{"bundle", "-o", "-", "--skip", "version,check"}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	return bundletest.Extract(t, strings.NewReader(stdout))
}

func TestWithBundleCollectors(t *testing.T) {
	env := newEnv(t)
	code, stdout, stderr := runRoot(t, env, "bundle", "--list-collectors")
	if code != 0 || !slices.Contains(strings.Fields(stdout), evidenceName) {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and %s listed", code, stdout, stderr, evidenceName)
	}
	a := bundleArchive(t, env)
	if _, ok := a.File("example/pod.yaml"); !ok {
		t.Error("archive lacks example/pod.yaml")
	}
	if skipped := bundleArchive(t, env, "--skip", evidenceName); func() bool { _, ok := skipped.File("example/pod.yaml"); return ok }() {
		t.Error("--skip did not leave the example collector out")
	}
}

// TestDownstreamCanaries checks the Sink contract at the downstream boundary:
// Pods come out as YAML with env values redacted; Secrets, ConfigMaps and
// kind-less objects are refused.
func TestDownstreamCanaries(t *testing.T) {
	env := newEnv(t)
	a := bundleArchive(t, env)
	bundletest.AssertNoCanary(t, a, []string{evidenceCanary})
	for _, name := range []string{"example/pod.yaml", "example/pods.yaml"} {
		b, ok := a.File(name)
		if !ok || !strings.Contains(string(b), "kind: Pod") || !strings.Contains(string(b), "value: '[redacted]'") {
			t.Errorf("%s = %q (found %v); want Pod YAML with the env value redacted", name, b, ok)
		}
	}
	if b, _ := a.File("example/pods.yaml"); !strings.Contains(string(b), "kind: List") {
		t.Errorf("example/pods.yaml = %q; want the List", b)
	}
	if _, ok := a.File("example/evidence.txt"); !ok {
		t.Error("archive lacks example/evidence.txt")
	}
	refused := map[string]string{
		"example/secret.yaml": "forbidden kind", "example/configmap.yaml": "forbidden kind",
		"example/secrets.yaml": "forbidden kind", "example/configmaps.yaml": "forbidden kind",
		"example/list-secret.yaml": "forbidden kind", "example/list-configmap.yaml": "forbidden kind",
		"example/kindless.yaml": "missing kind", "example/list-kindless.yaml": "missing kind",
	}
	for name := range refused {
		if _, ok := a.File(name); ok {
			t.Errorf("archive holds the refused item %s", name)
		}
	}
	var m struct {
		Item struct {
			Items []struct{ Path, Status, Reason string }
		}
	}
	mf, _ := a.File("manifest.json")
	if err := json.Unmarshal(mf, &m); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, it := range m.Item.Items {
		status[it.Path] = it.Status + ": " + it.Reason
	}
	for p, reason := range refused {
		if status[p] != "failed: "+reason {
			t.Errorf("%s = %q, want failed: %s (items: %v)", p, status[p], reason, status)
		}
	}
}

// checkReport is the part of check -o json these tests read.
type checkReport struct {
	Item struct {
		Installs []struct {
			Findings []struct{ Code, Severity, Message string }
			Checks   []struct {
				Name string
				OK   bool
				Hold *struct {
					Until  time.Time
					Count  int
					Reason string
				}
			}
		}
		Incomplete []struct{ Input, State string }
	}
}

// runCheck runs check -o json and returns the exit code and the one install's report.
func runCheck(t *testing.T, env *testenv.Env) (int, checkReport) {
	t.Helper()
	code, stdout, stderr := runRoot(t, env, "check", "-o", "json")
	var r checkReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if len(r.Item.Installs) != 1 {
		t.Fatalf("installs = %d, want the vendor install\n%s", len(r.Item.Installs), stdout)
	}
	for _, in := range r.Item.Incomplete {
		if in.Input == probeName {
			t.Errorf("collector input %s is %s, want ok", in.Input, in.State)
		}
	}
	return code, r
}

// serverStatus returns the report's example server check and its EXAMPLE-001 findings.
func serverStatus(t *testing.T, r checkReport) (ok bool, hold *struct {
	Until  time.Time
	Count  int
	Reason string
}, codes []string) {
	t.Helper()
	in := r.Item.Installs[0]
	found := false
	for _, c := range in.Checks {
		if c.Name == serverCheckName {
			found, ok, hold = true, c.OK, c.Hold
		}
	}
	if !found {
		t.Fatalf("checks have no %q: %+v", serverCheckName, in.Checks)
	}
	for _, f := range in.Findings {
		if f.Code == codeServer {
			codes = append(codes, f.Code+" "+f.Severity)
		}
	}
	return ok, hold, codes
}

func TestDownstreamCheckServerAbsent(t *testing.T) {
	env := newEnv(t)
	code, r := runCheck(t, env)
	_, hold, codes := serverStatus(t, r)
	if code != 1 || hold != nil || len(codes) != 1 || codes[0] != "EXAMPLE-001 warning" {
		t.Errorf("exit %d, hold %+v, findings %v; want 1, no hold, one EXAMPLE-001 warning", code, hold, codes)
	}
}

func TestDownstreamCheckServerUnreadyHolds(t *testing.T) {
	env := newEnv(t, "testdata/server-unready.yaml")
	start := time.Now()
	code, r := runCheck(t, env)
	_, hold, codes := serverStatus(t, r)
	if code != 0 || len(codes) != 0 {
		t.Errorf("exit %d, findings %v; want 0 and none while holding", code, codes)
	}
	if hold == nil || hold.Count != 1 || hold.Reason == "" || hold.Until.Before(start.Add(serverHold)) {
		t.Errorf("hold = %+v, want 1 held until at least %v from start", hold, serverHold)
	}
}

func TestDownstreamCheckServerUnreadyAfterHold(t *testing.T) {
	// A zero hold is already over at the first run: the unready server is a finding.
	prev := serverHold
	serverHold = 0
	t.Cleanup(func() { serverHold = prev })
	env := newEnv(t, "testdata/server-unready.yaml")
	code, r := runCheck(t, env)
	_, hold, codes := serverStatus(t, r)
	if code != 1 || hold != nil || len(codes) != 1 || codes[0] != "EXAMPLE-001 warning" {
		t.Errorf("exit %d, hold %+v, findings %v; want 1, no hold, one EXAMPLE-001 warning", code, hold, codes)
	}
}

func TestDownstreamCheckServerReady(t *testing.T) {
	env := newEnv(t, "testdata/server-ready.yaml")
	code, r := runCheck(t, env)
	ok, hold, codes := serverStatus(t, r)
	if code != 0 || !ok || hold != nil || len(codes) != 0 {
		t.Errorf("exit %d, ok %v, hold %+v, findings %v; want 0, ok, no hold, no findings", code, ok, hold, codes)
	}
}

func TestDownstreamCheckText(t *testing.T) {
	t.Run("finding", func(t *testing.T) {
		env := newEnv(t)
		code, stdout, stderr := runRoot(t, env, "check")
		if code != 1 || !strings.Contains(stdout, codeServer) || !strings.Contains(stdout, serverMissing) {
			t.Errorf("exit %d, stdout:\n%s\nstderr: %s\nwant 1 and the %s finding", code, stdout, stderr, codeServer)
		}
		t.Logf("text output:\n%s", stdout)
	})
	t.Run("hold", func(t *testing.T) {
		env := newEnv(t, "testdata/server-unready.yaml")
		code, stdout, stderr := runRoot(t, env, "check")
		if code != 0 || !strings.Contains(stdout, serverCheckName) || !strings.Contains(stdout, "1 held") || !strings.Contains(stdout, serverHoldReason) {
			t.Errorf("exit %d, stdout:\n%s\nstderr: %s\nwant 0 and the held check", code, stdout, stderr)
		}
		t.Logf("text output:\n%s", stdout)
	})
}
