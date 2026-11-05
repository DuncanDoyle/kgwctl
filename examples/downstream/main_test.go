package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

func newEnv(t *testing.T) *testenv.Env {
	t.Helper()
	return testenv.New(t, testenv.Options{
		Fixtures: []string{"testdata/install.yaml"},
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
	// TODO(Task 1.2.3): un-skip when `get` exists and assert the ExamplePolicy
	// with no ancestors appears in `get examplepolicies -A`.
	t.Skip("needs get, sub-phase 1.2")
}

func TestDownstreamCommand(t *testing.T) {
	env := newEnv(t)
	code, stdout, stderr := runRoot(t, env, "hello")
	if code != 0 || stdout != "hello\n" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and \"hello\\n\"", code, stdout, stderr)
	}
}
