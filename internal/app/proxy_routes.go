package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/render"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
)

// defaultMaxSize caps the config dump proxy routes parses (design §4.1).
const defaultMaxSize = "64Mi"

// routesOptions are proxy routes' own flags.
type routesOptions struct {
	file, gateway, pod, route, maxSize string
	rule                               int
	response                           bool
	output                             string
}

// newProxyRoutesCmd builds proxy routes (phase 3 design §3.1).
func newProxyRoutesCmd(now func() time.Time, hooks proxyTestHooks) *cobra.Command {
	var o routesOptions
	cmd := &cobra.Command{
		Use:   "routes [<gateway>]",
		Short: "Show a Gateway's listeners, routes and the HTTP filters each route runs",
		Long: "Show one Gateway's Envoy listeners, virtual hosts and routes, with the HTTP filters each route runs\n" +
			"in request order and the Gateway API and kgateway objects they come from.\n\n" +
			"Live, it reads the config dump of one replica, the oldest Ready one unless --pod names another,\n" +
			"and the cluster's HTTPRoutes. With --file it reads a saved dump and calls no Kubernetes API.\n" +
			"A route whose name reads as two HTTPRoutes is listed as ambiguous, never matched.",
		Example: "  kgwctl proxy routes http -n kgateway-system\n" +
			"  kgwctl proxy routes http -n kgateway-system --route team-a/orders --rule 0\n" +
			"  kgwctl proxy routes --file dump.json --gateway kgateway-system/http",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError(cmd, "proxy routes takes at most one Gateway name, got %d", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := FlagsFrom(cmd)
			if err != nil {
				return &ExitError{Code: 2, Err: err}
			}
			return proxyRoutes(cmd, f, now, hooks, args, o)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.file, "file", "", "Read a saved config dump instead of a live proxy; calls no Kubernetes API")
	fs.StringVar(&o.gateway, "gateway", "", "With --file, the Gateway `<ns>/<name>` the dump belongs to, to label listeners")
	fs.StringVar(&o.pod, "pod", "", "Read this replica of the Gateway, Ready or not")
	fs.StringVar(&o.route, "route", "", "Show only the routes of HTTPRoute `<ns>/<name>`; <name> alone uses -n or the context's namespace")
	fs.IntVar(&o.rule, "rule", 0, "With --route, show only this rule `index`")
	fs.BoolVar(&o.response, "response", false, "Also show each route's response order")
	fs.StringVarP(&o.output, "output", "o", print.FormatText, "Output format: text, json or yaml")
	fs.StringVar(&o.maxSize, "max-size", defaultMaxSize, "Largest config dump to parse, as a `quantity` such as 64Mi")
	return cmd
}

// usageError is an exit-2 error that ends with cmd's usage line.
func usageError(cmd *cobra.Command, format string, args ...any) error {
	return &ExitError{Code: 2, Err: fmt.Errorf(format+"\nUsage: %s", append(args, cmd.UseLine())...)}
}

// routesRequest is a validated proxy routes invocation.
type routesRequest struct {
	live     bool
	gateway  *types.NamespacedName // live: the argument's namespace is resolved later
	max      int64
	sel      proxyview.Selection
	routeNS  bool // --route named its namespace
	response bool
	output   string
}

// parseRoutes checks the flag combinations before any I/O.
func parseRoutes(cmd *cobra.Command, args []string, o routesOptions) (routesRequest, error) {
	req := routesRequest{live: o.file == "", response: o.response, output: o.output}
	switch {
	case o.output != print.FormatText && o.output != print.FormatJSON && o.output != print.FormatYAML:
		return req, usageError(cmd, "invalid --output %q: want text, json or yaml", o.output)
	case req.live && len(args) == 0:
		return req, usageError(cmd, "proxy routes needs a Gateway name, or --file <path>")
	case !req.live && len(args) == 1:
		return req, usageError(cmd, "give a Gateway name or --file, not both; with --file, --gateway <ns>/<name> names the Gateway")
	case req.live && o.gateway != "":
		return req, usageError(cmd, "--gateway works only with --file; live, name the Gateway as the argument")
	case !req.live && o.pod != "":
		return req, usageError(cmd, "--pod works only live, not with --file")
	}
	if o.gateway != "" {
		gw, ok := splitRef(o.gateway)
		if !ok || gw.Namespace == "" {
			return req, usageError(cmd, "invalid --gateway %q: want <ns>/<name>", o.gateway)
		}
		req.gateway = &gw
	}
	q, err := resource.ParseQuantity(o.maxSize)
	if err != nil || q.Sign() <= 0 {
		return req, usageError(cmd, "invalid --max-size %q: want a positive quantity such as 64Mi", o.maxSize)
	}
	req.max = q.Value()
	if cmd.Flags().Changed("rule") {
		switch {
		case o.route == "":
			return req, usageError(cmd, "--rule needs --route")
		case o.rule < 0:
			return req, usageError(cmd, "invalid --rule %d: must not be negative", o.rule)
		}
		req.sel.Rule = &o.rule
	}
	if o.route != "" {
		r, ok := splitRef(o.route)
		if !ok {
			return req, usageError(cmd, "invalid --route %q: want <ns>/<name> or <name>", o.route)
		}
		req.sel.Route, req.routeNS = r, r.Namespace != ""
	}
	return req, nil
}

// splitRef parses "<ns>/<name>" or "<name>".
func splitRef(s string) (types.NamespacedName, bool) {
	parts := strings.Split(s, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return types.NamespacedName{Name: parts[0]}, true
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return types.NamespacedName{Namespace: parts[0], Name: parts[1]}, true
	}
	return types.NamespacedName{}, false
}

func proxyRoutes(cmd *cobra.Command, f *Flags, now func() time.Time, hooks proxyTestHooks, args []string, o routesOptions) error {
	req, err := parseRoutes(cmd, args, o)
	if err != nil {
		return err
	}
	if !req.live {
		return routesFromFile(cmd, f, req, o.file)
	}
	return routesLive(cmd, f, now, hooks, req, args[0], o.pod)
}

// routeNamespace fills a --route without a namespace from ns.
func routeNamespace(req *routesRequest, ns string) error {
	if req.sel.Route.Name == "" || req.routeNS {
		return nil
	}
	if ns == "" {
		return &ExitError{Code: 2, Err: errors.New("--route needs <ns>/<name> with -A")}
	}
	req.sel.Route.Namespace = ns
	return nil
}

// routesFromFile reads a saved dump. It reads the kubeconfig at most for a
// --route namespace, and never calls Kubernetes (D-021).
func routesFromFile(cmd *cobra.Command, f *Flags, req routesRequest, path string) error {
	if req.sel.Route.Name != "" && !req.routeNS {
		ns, err := f.ContextNamespace()
		if err != nil {
			return &ExitError{Code: 2, Err: err}
		}
		if err := routeNamespace(&req, ns); err != nil {
			return err
		}
	}
	fh, err := os.Open(path)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	body, err := proxydump.ReadCapped(fh, req.max)
	_ = fh.Close()
	switch {
	case errors.Is(err, proxydump.ErrTooLarge):
		return tooLarge(path, req.max, false)
	case err != nil:
		return &ExitError{Code: 2, Err: fmt.Errorf("read %s: %w", path, err)}
	}
	header := "File " + path
	if req.gateway != nil {
		header += " · Gateway " + req.gateway.String()
	}
	src := proxyview.Source{File: path}
	if req.gateway != nil {
		src.Gateway = gatewayRef(*req.gateway)
	}
	return printRoutes(cmd, body, path, req.gateway, nil, req, header, src)
}

func routesLive(cmd *cobra.Command, f *Flags, now func() time.Time, hooks proxyTestHooks, req routesRequest, name, pod string) error {
	rc, ns, err := f.RESTConfig()
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	if hooks.WrapTransport != nil {
		rc.Wrap(hooks.WrapTransport)
	}
	if ns == "" {
		return &ExitError{Code: 2, Err: errors.New("proxy routes needs a namespace: use -n, not -A")}
	}
	if err := routeNamespace(&req, ns); err != nil {
		return err
	}
	gw := types.NamespacedName{Namespace: ns, Name: name}
	ctx, cancel := f.Context(cmd.Context())
	defer cancel()
	c, err := load.NewClients(ctx, rc)
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	d := hooks.Dialer
	if d == nil {
		d = proxydump.NewPortForwarder(rc)
	}

	capture, err := proxydump.FetchGateway(ctx, c, d, gw, pod, now)
	if err != nil {
		return dumpError(ctx, f, "proxy routes", gw, captureFailed(err))
	}
	// Reading the capped body here closes the tunnel at once; the read ends with ctx.
	body, err := proxydump.ReadCapped(capture.Body, req.max)
	_ = capture.Body.Close()
	r := dumpResult{capture: capture, body: body, stage: stageFetch, target: capture.Pod}
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(err, proxydump.ErrTooLarge):
		return tooLarge("the config dump of Pod "+r.target.String(), req.max, true)
	case err != nil:
		r.err = fmt.Errorf("read config dump from %s: %w", r.target, err)
		return dumpError(ctx, f, "proxy routes", gw, r)
	}

	known, err := knownHTTPRoutesByCtx(ctx, c)
	switch {
	case err != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return &ExitError{Code: 2, Err: fmt.Errorf("could not finish loading the HTTPRoutes of the cluster within --%s %v (the config dump of Pod %s was read)", flagRequestTimeout, f.RequestTimeout, r.target)}
	case err != nil && ctx.Err() != nil:
		return dumpError(ctx, f, "proxy routes", gw, dumpResult{stage: stageFetch, target: r.target, err: err})
	case err != nil:
		// Without the list, origins come from the dump alone; nothing ambiguous becomes a match.
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not list HTTPRoutes: %v\n", err)
	}
	header := fmt.Sprintf("Gateway %s · pod %s · %s", gw, r.target, r.capture.CapturedAt.UTC().Format(time.RFC3339))
	at := r.capture.CapturedAt.UTC()
	src := proxyview.Source{Gateway: gatewayRef(gw), Pod: &model.Ref{Kind: "Pod", Namespace: r.target.Namespace, Name: r.target.Name}, CapturedAt: &at}
	return printRoutes(cmd, r.body, "Pod "+r.target.String(), &gw, known, req, header, src)
}

// tooLarge reports a dump over --max-size (design §4.1). A live dump can also
// be saved and read with --file, whose own cap is --max-size.
func tooLarge(what string, max int64, live bool) error {
	q := resource.NewQuantity(max, resource.BinarySI)
	advice := "raise --max-size to read it"
	if live {
		advice = "raise --max-size, or save the dump with kgwctl proxy dump and read it with --file <path> --max-size <n>"
	}
	return &ExitError{Code: 2, Err: fmt.Errorf("%s is larger than --max-size %s: %s", what, q, advice)}
}

// httpRoutesGVR is the HTTPRoute resource listed for live origins.
var httpRoutesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// knownHTTPRoutesByCtx runs knownHTTPRoutes so that it returns ctx's error at
// ctx's end even when the list ignores ctx, as an exec credential plugin does.
func knownHTTPRoutesByCtx(ctx context.Context, c load.Clients) ([]model.Ref, error) {
	type result struct {
		known []model.Ref
		err   error
	}
	done := make(chan result, 1)
	go func() {
		known, err := knownHTTPRoutes(ctx, c)
		done <- result{known, err}
	}()
	select {
	case r := <-done:
		return r.known, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// knownHTTPRoutes lists the cluster's HTTPRoutes in all namespaces, page by
// page (design §4.2). It lists no other kind, so nothing else can delay it.
func knownHTTPRoutes(ctx context.Context, c load.Clients) ([]model.Ref, error) {
	var known []model.Ref
	opts := metav1.ListOptions{Limit: 500}
	for {
		list, err := c.Dynamic.Resource(httpRoutesGVR).Namespace(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, u := range list.Items {
			known = append(known, proxyview.HTTPRouteRef(types.NamespacedName{Namespace: u.GetNamespace(), Name: u.GetName()}))
		}
		if opts.Continue = list.GetContinue(); opts.Continue == "" {
			return known, nil
		}
	}
}

// gatewayRef is the envelope's reference to a Gateway.
func gatewayRef(n types.NamespacedName) *model.Ref {
	return &model.Ref{Group: "gateway.networking.k8s.io", Kind: "Gateway", Namespace: n.Namespace, Name: n.Name}
}

// printRoutes builds the view from body and prints the tree, or the envelope
// for -o json|yaml, with src as its source.
func printRoutes(cmd *cobra.Command, body []byte, source string, gw *types.NamespacedName, known []model.Ref, req routesRequest, header string, src proxyview.Source) error {
	v, err := proxyview.Build(body, gw, known, req.sel)
	if errors.Is(err, proxyview.ErrNotConfigDump) {
		return &ExitError{Code: 2, Err: fmt.Errorf("%s: %w", source, err)}
	}
	if err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("parse config dump of %s: %w", source, err)}
	}
	if req.output != print.FormatText {
		if err := print.Envelope(cmd.OutOrStdout(), req.output, "ProxyRoutes", proxyview.Item{Source: src, View: v}); err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("write routes: %w", err)}
		}
		return nil
	}
	if err := proxyview.Text(cmd.OutOrStdout(), v, render.Options{Header: header, ResponseOrder: req.response}); err != nil {
		return &ExitError{Code: 2, Err: fmt.Errorf("write routes: %w", err)}
	}
	return nil
}
