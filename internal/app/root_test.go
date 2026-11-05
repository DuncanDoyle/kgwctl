package app_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

func run(root *cobra.Command, args ...string) int {
	root.SetArgs(args)
	return app.Execute(root)
}

func TestContextFlagWins(t *testing.T) {
	env := testenv.New(t, testenv.Options{Contexts: 2})
	var out, errb bytes.Buffer
	root := app.NewRoot(app.Config{Out: &out, Err: &errb})
	code := run(root, "--kubeconfig", env.Kubeconfig, "--context", testenv.ContextName(1), "version")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errb.String())
	}
	if n := len(env.Proxies[0].Requests()); n != 0 {
		t.Errorf("current-context proxy got %d requests, want 0: %v", n, env.Proxies[0].Requests())
	}
	reqs := env.Proxies[1].Requests()
	disc := false
	for _, r := range reqs {
		if strings.HasPrefix(r, "GET /apis/") || strings.HasPrefix(r, "GET /api/v1") {
			disc = true
		}
	}
	if len(reqs) == 0 || !disc {
		t.Errorf("selected-context proxy requests = %v, want discovery and loads", reqs)
	}
}

func TestNamespaceDefaultsToContext(t *testing.T) {
	env := testenv.New(t, testenv.Options{Contexts: 2})
	kc, err := clientcmd.LoadFromFile(env.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kc.Contexts[testenv.ContextName(1)].Namespace = "team-a"
	path := t.TempDir() + "/kubeconfig"
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		f    app.Flags
		want string
	}{
		{"context namespace", app.Flags{Kubeconfig: path, ContextName: testenv.ContextName(1)}, "team-a"},
		{"-n overrides", app.Flags{Kubeconfig: path, ContextName: testenv.ContextName(1), Namespace: "x"}, "x"},
		{"-A selects all", app.Flags{Kubeconfig: path, ContextName: testenv.ContextName(1), AllNamespaces: true}, ""},
		{"default namespace", app.Flags{Kubeconfig: path, ContextName: testenv.ContextName(0)}, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ns, err := tc.f.RESTConfig()
			if err != nil {
				t.Fatal(err)
			}
			if ns != tc.want {
				t.Errorf("namespace = %q, want %q", ns, tc.want)
			}
		})
	}
}

func TestFlagsParse(t *testing.T) {
	var got *app.Flags
	// A fresh command per root: a command keeps the flag values it parsed.
	probe := func() *cobra.Command {
		return &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			got, err = app.FlagsFrom(cmd)
			return err
		}}
	}
	root := app.NewRoot(app.Config{Commands: []*cobra.Command{probe()}})
	if code := run(root, "-A", "-n", "ns", "--request-timeout", "5s", "--install", "a/b", "--all-controllers", "probe"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !got.AllNamespaces || got.Namespace != "ns" || got.RequestTimeout.Seconds() != 5 || got.Install != "a/b" || !got.AllControllers {
		t.Errorf("flags = %+v", got)
	}
	root = app.NewRoot(app.Config{Commands: []*cobra.Command{probe()}})
	if code := run(root, "probe"); code != 0 || got.RequestTimeout.Seconds() != 30 {
		t.Errorf("default request timeout = %v, want 30s", got.RequestTimeout)
	}
}

func TestExecuteExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	boom := &cobra.Command{Use: "boom", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }}
	root := app.NewRoot(app.Config{Commands: []*cobra.Command{boom}, Out: &out, Err: &errb})
	if code := run(root, "boom"); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if got := errb.String(); got != "Error: boom\n" {
		t.Errorf("stderr = %q, want %q", got, "Error: boom\n")
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

func TestExecuteUnknownFlagIsError(t *testing.T) {
	var out, errb bytes.Buffer
	root := app.NewRoot(app.Config{Out: &out, Err: &errb})
	if code := run(root, "--nope"); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.HasPrefix(errb.String(), "Error: ") || strings.Contains(errb.String(), "Usage:") || out.Len() != 0 {
		t.Errorf("stderr = %q, stdout = %q", errb.String(), out.String())
	}
}

func TestRequestTimeoutZeroMeansNoDeadline(t *testing.T) {
	f := app.Flags{}
	ctx, cancel := f.Context(t.Context())
	defer cancel()
	if _, ok := ctx.Deadline(); ok || ctx.Err() != nil {
		t.Errorf("timeout 0: deadline set or ctx done (%v)", ctx.Err())
	}
	cancel()
	if ctx.Err() == nil {
		t.Error("cancel did not cancel the child context")
	}
	if t.Context().Err() != nil {
		t.Error("cancel reached the parent")
	}
	f.RequestTimeout = time.Hour
	ctx2, cancel2 := f.Context(t.Context())
	defer cancel2()
	if _, ok := ctx2.Deadline(); !ok {
		t.Error("positive timeout set no deadline")
	}
}

func TestNegativeRequestTimeoutIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	ran := false
	cmd := &cobra.Command{Use: "probe", RunE: func(c *cobra.Command, _ []string) error {
		_, err := app.FlagsFrom(c)
		ran = true
		return err
	}}
	root := app.NewRoot(app.Config{Commands: []*cobra.Command{cmd}, Out: &out, Err: &errb})
	if code := run(root, "--request-timeout=-1s", "probe"); code != 1 || !ran {
		t.Errorf("exit = %d, ran = %v, want 1", code, ran)
	}
	if !strings.Contains(errb.String(), "Error: invalid --request-timeout") || out.Len() != 0 {
		t.Errorf("stderr = %q, stdout = %q", errb.String(), out.String())
	}
}

func TestFlagsFromUnregisteredIsError(t *testing.T) {
	if _, err := app.FlagsFrom(&cobra.Command{Use: "bare"}); err == nil {
		t.Error("FlagsFrom on a command without the global flags returned no error")
	}
}
