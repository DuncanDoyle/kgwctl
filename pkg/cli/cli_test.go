package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/proxystats"
	"github.com/DuncanDoyle/xdsview/pkg/adapter/kgateway"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/pkg/bundle"
	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

func TestCLIName(t *testing.T) {
	root := cli.NewRootCommand(cli.WithName("vendorctl"))
	if root.Use != "vendorctl" {
		t.Fatalf("Use = %q, want vendorctl", root.Use)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"completion", "bash"})
	if code := cli.Execute(root); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "vendorctl") || strings.Contains(out.String(), "kgwctl") {
		t.Error("completion script does not use the configured name")
	}
}

func TestDefaults(t *testing.T) {
	if got := cli.NewRootCommand().Use; got != "kgwctl" {
		t.Errorf("default name = %q, want kgwctl", got)
	}
}

func TestWithCommands(t *testing.T) {
	var ran bool
	extra := &cobra.Command{Use: "extra", Run: func(*cobra.Command, []string) { ran = true }}
	root := cli.NewRootCommand(cli.WithCommands(extra))
	root.SetArgs([]string{"extra"})
	if code := cli.Execute(root); code != 0 || !ran {
		t.Errorf("exit %d, ran %v", code, ran)
	}
}

type collector struct{}

func (collector) Name() string                                { return "probe" }
func (collector) Start(context.Context, extension.Deps) error { return nil }

// hints is a command hints type other than the default registry's.
type hints struct{ diagnose.Commands }

// denyAll is an authorizer other than the default registry's.
type denyAll struct{}

func (denyAll) Authorize(*http.Request, objects.Ref) bool { return false }

func TestIgnoredCapabilitiesLogOneDebugLineEach(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	// check runs collectors, the proxy stats source and command hints, so none is ignored.
	reg := extension.New(extension.WithCollectors(collector{}), extension.WithProxyStatsSource(proxystats.Kube{}),
		extension.WithCommands(hints{}), extension.WithAuthorizer(denyAll{}))
	cli.NewRootCommand(cli.WithRegistry(reg))
	if n := strings.Count(buf.String(), "ignored registry capability"); n != 1 || !strings.Contains(buf.String(), "capability=authorizer") {
		t.Errorf("got %d debug lines, want 1 for the authorizer:\n%s", n, buf.String())
	}
	buf.Reset()
	cli.NewRootCommand()
	if buf.Len() != 0 {
		t.Errorf("default registry logged: %s", buf.String())
	}
}

// bundleProbe is a bundle collector that writes nothing.
type bundleProbe string

func (p bundleProbe) Name() string                                         { return string(p) }
func (bundleProbe) Collect(context.Context, bundle.Env, bundle.Sink) error { return nil }

// listCollectors runs `bundle --list-collectors` and returns the exit code and output.
func listCollectors(t *testing.T, cs ...bundle.Collector) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	root := cli.NewRootCommand(cli.WithBundleCollectors(cs...))
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"bundle", "--list-collectors"})
	code = cli.Execute(root)
	return code, out.String(), errb.String()
}

func TestWithBundleCollectors(t *testing.T) {
	t.Run("listed after the built-in collectors", func(t *testing.T) {
		code, stdout, stderr := listCollectors(t, bundleProbe("alpha"), bundleProbe("beta"))
		names := strings.Fields(stdout)
		if code != 0 || len(names) < 3 || names[len(names)-2] != "alpha" || names[len(names)-1] != "beta" || !slices.Contains(names, "logs") {
			t.Errorf("exit %d, names %v, stderr %q; want 0 and the built-ins then alpha, beta", code, names, stderr)
		}
	})
	for _, tc := range []struct {
		name string
		cs   []bundle.Collector
		want string
	}{
		{"built-in name", []bundle.Collector{bundleProbe("logs")}, `duplicate collector name "logs"`},
		{"two downstream collectors", []bundle.Collector{bundleProbe("alpha"), bundleProbe("alpha")}, `duplicate collector name "alpha"`},
		{"invalid name", []bundle.Collector{bundleProbe("Not Valid")}, `invalid collector name "Not Valid"`},
	} {
		t.Run(tc.name+" is an error", func(t *testing.T) {
			code, stdout, stderr := listCollectors(t, tc.cs...)
			if code == 0 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want an error with %q and no list", code, stdout, stderr, tc.want)
			}
		})
	}
}

// linkField wraps the kgateway module under its own name, as a downstream build
// does, and links the record of exampleField to the example/filter slot.
type linkField struct{ inner provenance.Module }

func (linkField) Name() string { return "example" }

func (m linkField) Claims(snap *model.Snapshot) []provenance.Claim {
	inner := m.inner.Claims(snap)
	var slot model.ID
	for _, f := range snap.Listeners[0].FilterChains[0].HCM.HTTPFilters {
		if f.Name == "example/filter" {
			slot = f.ID
		}
	}
	out := inner
	for _, c := range inner {
		rec, ok := c.Assertion.(provenance.PolicyRecord)
		if !ok || rec.Field != "exampleField" {
			continue
		}
		out = append(out, provenance.Claim{Subject: c.Subject, Certainty: model.Inferred,
			Assertion: provenance.FilterLink{Record: c.Ref, Slot: slot},
			Evidence:  []provenance.Evidence{{Kind: provenance.EvidenceMetadata, Subject: c.Subject, Field: "metadata.filter_metadata.example", Object: &rec.Policy}}})
	}
	return out
}

// named is a module that only has a name.
type named string

func (n named) Name() string                            { return string(n) }
func (named) Claims(*model.Snapshot) []provenance.Claim { return nil }

// runRoutes runs `proxy routes` on the downstream-field dump and returns the exit code and output.
func runRoutes(t *testing.T, root *cobra.Command, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"proxy", "routes", "--file", "../../internal/proxyview/testdata/downstream-field.json", "--route", "shop/orders"}, args...))
	code = cli.Execute(root)
	return code, out.String(), errb.String()
}

// filterRow returns the text row of the example/filter slot.
func filterRow(t *testing.T, out string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "example/filter") {
			return l
		}
	}
	t.Fatalf("no example/filter row:\n%s", out)
	return ""
}

// TestWithProvenanceModules: R12a, R13 and the empty call, at the command boundary.
func TestWithProvenanceModules(t *testing.T) {
	const policiesLine = "policies: TrafficPolicy ns/p (exampleField)\n"
	t.Run("a wrapper links an inner record to a slot", func(t *testing.T) {
		code, stdout, stderr := runRoutes(t, cli.NewRootCommand(cli.WithProvenanceModules(linkField{kgateway.New()})))
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if row := filterRow(t, stdout); !strings.HasSuffix(row, " · TrafficPolicy ns/p (exampleField, inferred)") {
			t.Errorf("filter row %q lacks the linked record", row)
		}
		if !strings.Contains(stdout, policiesLine) {
			t.Errorf("stdout lacks %q:\n%s", policiesLine, stdout)
		}
		code, stdout, _ = runRoutes(t, cli.NewRootCommand(cli.WithProvenanceModules(linkField{kgateway.New()})), "-o", "json")
		var doc struct {
			Item struct {
				Listeners []struct {
					FilterChains []struct {
						HCM struct {
							VirtualHosts []struct {
								Routes []struct {
									Filters []struct {
										Name     string
										Policies []struct {
											Policy    struct{ Kind, Namespace, Name string }
											Field     string
											Level     string
											Certainty string
										}
									}
								}
							}
						}
					}
				}
			}
		}
		if err := json.Unmarshal([]byte(stdout), &doc); code != 0 || err != nil {
			t.Fatalf("exit %d, decode %v", code, err)
		}
		var found bool
		for _, f := range doc.Item.Listeners[0].FilterChains[0].HCM.VirtualHosts[0].Routes[0].Filters {
			if f.Name != "example/filter" {
				continue
			}
			found = len(f.Policies) == 1 && f.Policies[0].Policy.Kind == "TrafficPolicy" && f.Policies[0].Policy.Namespace == "ns" &&
				f.Policies[0].Policy.Name == "p" && f.Policies[0].Field == "exampleField" && f.Policies[0].Level == "route" && f.Policies[0].Certainty == "inferred"
		}
		if !found {
			t.Errorf("example/filter policies lack the linked record:\n%s", stdout)
		}
	})
	t.Run("the default build shows the record on the policies line only", func(t *testing.T) {
		code, stdout, stderr := runRoutes(t, cli.NewRootCommand())
		if code != 0 || stderr != "" || !strings.Contains(stdout, policiesLine) || strings.Contains(filterRow(t, stdout), "TrafficPolicy") {
			t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
		}
	})
	t.Run("no modules keeps the default", func(t *testing.T) {
		for _, args := range [][]string{nil, {"-o", "json"}} {
			_, def, _ := runRoutes(t, cli.NewRootCommand(), args...)
			code, got, stderr := runRoutes(t, cli.NewRootCommand(cli.WithProvenanceModules()), args...)
			if code != 0 || stderr != "" || got != def {
				t.Errorf("%v: exit %d, stderr %q, stdout:\n%s\nwant:\n%s", args, code, stderr, got, def)
			}
		}
	})
	t.Run("two modules with one name exit 2", func(t *testing.T) {
		root := func() *cobra.Command {
			return cli.NewRootCommand(cli.WithProvenanceModules(named("example"), named("example")))
		}
		code, stdout, stderr := runRoutes(t, root())
		if code != 2 || stdout != "" || !strings.Contains(stderr, "compose provenance modules") {
			t.Errorf("exit %d, stdout %q, stderr %q; want 2 and compose provenance modules", code, stdout, stderr)
		}
		// Other commands do not compose.
		var out bytes.Buffer
		r := root()
		r.SetOut(&out)
		r.SetArgs([]string{"version", "--client"})
		if code := cli.Execute(r); code != 0 {
			t.Errorf("version --client: exit %d", code)
		}
	})
}
