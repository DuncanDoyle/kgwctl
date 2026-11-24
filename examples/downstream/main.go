// Command vendorctl is a downstream CLI built only on pkg/cli. It registers a
// pack with a policy kind, an install detector, a collector and a check, and
// a command, to show the extension seam.
package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/kgwctl/pkg/bundle"
	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

// controllerName is the GatewayClass controller name of the example distribution.
const controllerName = "example.com/gateway-controller"

// The example pack's names. A check reads the collector's value as "<collector>/<key>".
const (
	packName         = "example"
	probeName        = "example-probe"
	probeKey         = probeName + "/servers"
	serverCheckName  = "example server"
	codeServer       = "EXAMPLE-001"
	serverMissing    = "Example server not running"
	serverNotReady   = "Example server not ready"
	serverHoldReason = "Waiting to see if the example server becomes ready"
)

// The example bundle collector's name and the secret-shaped value it plants to show the Sink's guarantees.
const (
	evidenceName   = "example-evidence"
	evidenceCanary = "kgwctl-canary-example"
)

// serverHold is how long an unready server is held before it is a finding; tests shorten it.
var serverHold = 30 * time.Second

// serverSelector selects the example server's Pods in the install's namespace.
var serverSelector = labels.SelectorFromSet(labels.Set{"app": "example-server"})

func main() {
	os.Exit(cli.Execute(newRoot()))
}

// newRoot builds the vendorctl root command; main and the tests share it.
func newRoot() *cobra.Command {
	return cli.NewRootCommand(
		cli.WithName("vendorctl"),
		cli.WithRegistry(newRegistry()),
		cli.WithBundleCollectors(evidence{}),
		cli.WithCommands(&cobra.Command{
			Use:   "hello",
			Short: "Print hello",
			Args:  cobra.NoArgs,
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), "hello") },
		}),
	)
}

// evidence is the example bundle collector. It writes a secret-shaped value through
// JSON and Text, which the Sink redacts, then tries two writes the Sink refuses.
type evidence struct{}

func (evidence) Name() string { return evidenceName }

// Collect writes two typed items, then a raw payload and a Secret. The Sink
// records each refusal as a failed item, so Collect ignores those errors.
func (evidence) Collect(ctx context.Context, _ bundle.Env, s bundle.Sink) error {
	if err := s.JSON(ctx, bundle.Path{"example", "evidence.json"},
		strings.NewReader(`{"service":"example","token":"`+evidenceCanary+`"}`), bundle.Source{}); err != nil {
		return err
	}
	if err := s.Text(ctx, bundle.Path{"example", "evidence.txt"},
		strings.NewReader("GET /callback?access_token="+evidenceCanary+"&state=abc\n"), bundle.Source{}); err != nil {
		return err
	}
	// There is no raw write: a payload that is no JSON document is refused.
	_ = s.JSON(ctx, bundle.Path{"example", "payload.json"}, strings.NewReader("raw payload "+evidenceCanary), bundle.Source{})
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "example", "namespace": "default"},
		"data": map[string]any{"token": evidenceCanary},
	}}
	_ = s.Object(ctx, bundle.Path{"example", "secret.yaml"}, secret, bundle.Source{})
	return nil
}

// newRegistry adds the example pack to the defaults: the ExamplePolicy kind,
// the example detector, the server probe and the server check.
func newRegistry() *extension.Registry {
	return extension.New(extension.WithPack(extension.Pack{
		Name: packName,
		Kinds: []kinds.Descriptor{{
			Group: "policy.example.com", Kind: "ExamplePolicy", Resource: "examplepolicies",
			Versions: []string{"v1"}, Namespaced: true, Category: kinds.CategoryPolicy, DisplayName: "ExamplePolicy",
		}},
		Detectors:  []install.Detector{detector{}},
		Checks:     []health.Check{serverCheck{}},
		Collectors: []func(extension.CollectorEnv) extension.Collector{func(env extension.CollectorEnv) extension.Collector { return serverProbe{env} }},
	}))
}

// detector recognises the example controller: Deployments labelled
// app=vendor-controller whose image repository is "vendor-controller".
type detector struct{}

func (detector) Name() string { return "example" }

func (detector) ControllerSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-controller"})
}

func (detector) ControllerPodSelector() labels.Selector { return detector{}.ControllerSelector() }

func (detector) ProxySelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-proxy"})
}

func (detector) Detect(d *appsv1.Deployment) (install.Install, bool) {
	for _, c := range d.Spec.Template.Spec.Containers {
		repo := c.Image[strings.LastIndex(c.Image, "/")+1:]
		repo, tag, _ := strings.Cut(repo, ":")
		if repo != "vendor-controller" {
			continue
		}
		version := d.Labels["app.kubernetes.io/version"]
		if version == "" {
			version = strings.TrimPrefix(tag, "v")
		}
		return install.Install{
			Namespace: d.Namespace, Name: d.Name, Edition: "example", Version: version, Container: c.Name,
			ControllerNames: []string{controllerName}, Deployment: d, ProxySelector: detector{}.ProxySelector(),
		}, true
	}
	return install.Install{}, false
}

// serverPods counts one install's example server Pods by readiness.
type serverPods struct{ Ready, Unready int }

// serverProbe lists the example server Pods of each example install every
// interval and publishes their readiness under probeKey, keyed by install ID.
type serverProbe struct{ env extension.CollectorEnv }

func (serverProbe) Name() string { return probeName }

// Start probes once the first install is discovered, then every interval, until ctx ends.
func (p serverProbe) Start(ctx context.Context, d extension.Deps) error {
	if p.env.Discovered != nil {
		select {
		case <-ctx.Done():
			return nil
		case <-p.env.Discovered:
		}
	}
	clk := p.env.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	t := clk.NewTicker(p.env.Interval)
	defer t.Stop()
	for {
		p.probe(ctx, d)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
		}
	}
}

// probe publishes every example install's server Pods, or fails the key on a list error.
func (p serverProbe) probe(ctx context.Context, d extension.Deps) {
	out := map[string]serverPods{}
	for _, in := range p.env.Installs() {
		if in.Impl != p.env.Pack {
			continue
		}
		pods, err := p.env.Kube.CoreV1().Pods(in.Namespace).List(ctx, metav1.ListOptions{LabelSelector: serverSelector.String()})
		if err != nil {
			if ctx.Err() == nil {
				d.Fail("servers", collect.Failing, "list example server pods: "+err.Error())
			}
			return
		}
		var sp serverPods
		for i := range pods.Items {
			if podReady(&pods.Items[i]) {
				sp.Ready++
			} else {
				sp.Unready++
			}
		}
		out[in.ID()] = sp
	}
	h := fnv.New64a()
	for _, id := range slices.Sorted(maps.Keys(out)) {
		fmt.Fprintf(h, "%s=%d/%d;", id, out[id].Ready, out[id].Unready)
	}
	d.Publish("servers", out, h.Sum64(), p.env.MaxAge)
}

// podReady reports whether pod's Ready condition is True.
func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// serverCheck reports EXAMPLE-001 when the install has no example server Pod,
// or none has been Ready for serverHold since one was first seen unready.
type serverCheck struct{}

func (serverCheck) Name() string { return serverCheckName }

// Inputs declares the probe's value; Run may read no other collector key.
func (serverCheck) Inputs() health.InputSet { return health.InputSet{Collected: []string{probeKey}} }

// Run reads the install's server Pods from the probe and holds while they may still become ready.
func (c serverCheck) Run(ctx health.RunContext) ([]health.Finding, health.CheckResult) {
	in := ctx.Input
	v, ok := ctx.Collected(probeKey)
	switch {
	case ok && v.State != collect.OK:
		return nil, health.NotAvailable(c.Name(), health.AvailFailing, v.Reason, in.Now)
	case !ok:
		return nil, health.NotAvailable(c.Name(), health.AvailNotObserved, "example servers not probed yet", in.Now)
	}
	sp, probed := v.Data.(map[string]serverPods)[in.Install.ID()]
	if !probed {
		return nil, health.NotAvailable(c.Name(), health.AvailNotObserved, "example servers not probed yet", in.Now)
	}
	result := health.CheckResult{Name: c.Name(), OK: true, Required: true, CollectedAt: v.At}
	finding := func(msg, detail string) []health.Finding {
		return []health.Finding{{Code: codeServer, Slug: "example-server", Severity: health.SeverityWarning, Install: in.Install.ID(),
			Message: msg, Detail: detail, Key: []string{c.Name()}}}
	}
	if sp.Ready > 0 || sp.Unready == 0 {
		ctx.State.Delete("unreadySince")
		if sp.Ready == 0 {
			return finding(serverMissing, "no Pod labelled app=example-server in the install's namespace"), result
		}
		return nil, result
	}
	// No server is Ready: hold from when that was first seen, then report it.
	since, seen := health.Load[time.Time](ctx.State, "unreadySince")
	if !seen {
		since = in.Now
		ctx.State.Set("unreadySince", since)
	}
	if until := since.Add(serverHold); in.Now.Before(until) {
		ctx.Hold(until, sp.Unready, serverHoldReason)
		return nil, result
	}
	return finding(serverNotReady, fmt.Sprintf("%d example server Pods, none Ready", sp.Unready)), result
}
