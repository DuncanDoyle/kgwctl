package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/proxydump"
)

// proxyTestHooks replace proxy's dialer in tests; the zero value is production.
type proxyTestHooks struct {
	// Dialer, if set, replaces the port-forward to the admin port.
	Dialer proxydump.Dialer
	// WrapTransport, if set, wraps the Kubernetes transport of proxy routes.
	WrapTransport func(http.RoundTripper) http.RoundTripper
}

// redactionNote is phase 3 design §3.2's warning, shown in help and the README.
const redactionNote = "Envoy redacts only the fields it knows are sensitive, and only in strongly typed config. " +
	"The dump can still contain credentials, tokens or keys in other config. " +
	"Check it before you attach it to a ticket."

// newProxyCmd builds the proxy command group (phase 3 design §3).
func newProxyCmd(now func() time.Time, hooks proxyTestHooks) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Read what a Gateway's Envoy proxy holds",
		Long: "Read what a Gateway's Envoy proxy holds, through a port-forward to its admin port.\n\n" +
			"Needs create pods/portforward in the Gateway's namespace: see docs/rbac.yaml.",
		// Runnable, so cobra reaches Args instead of printing help for any argument.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return &ExitError{Code: 2, Err: fmt.Errorf("unknown command %q for %q\nRun '%s --help' for usage", args[0], cmd.CommandPath(), cmd.CommandPath())}
		},
	}
	// Proxy commands exit 0 or 2 (design §3.3); dump and routes inherit this.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &ExitError{Code: 2, Err: fmt.Errorf("%w\nUsage: %s", err, c.UseLine())}
	})
	cmd.AddCommand(newProxyDumpCmd(hooks), newProxyRoutesCmd(now, hooks))
	return cmd
}

// newProxyDumpCmd builds proxy dump (phase 3 design §3.2).
func newProxyDumpCmd(hooks proxyTestHooks) *cobra.Command {
	var pod string
	cmd := &cobra.Command{
		Use:   "dump <gateway>",
		Short: "Stream one replica's Envoy config dump to stdout",
		Long: "Stream Envoy's config dump of one replica of the Gateway to stdout, unchanged.\n" +
			"It picks the oldest Ready replica; --pod names another replica of the same Gateway.\n\n" +
			redactionNote,
		Example: "  kgwctl proxy dump http -n kgateway-system > dump.json",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &ExitError{Code: 2, Err: fmt.Errorf("proxy dump takes one Gateway name, got %d\nUsage: %s", len(args), cmd.UseLine())}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := FlagsFrom(cmd)
			if err != nil {
				return &ExitError{Code: 2, Err: err}
			}
			return proxyDump(cmd, f, hooks, args[0], pod)
		},
	}
	cmd.Flags().StringVar(&pod, "pod", "", "Dump this replica of the Gateway, Ready or not")
	return cmd
}

// dumpStage names where a dump failed, for the RBAC hint.
type dumpStage int

const (
	stageSelect dumpStage = iota
	stageFetch
)

// dumpResult is how far a dump got.
type dumpResult struct {
	capture *proxydump.Capture
	body    []byte // the capped body, read by proxy routes
	stage   dumpStage
	target  types.NamespacedName
	err     error
}

// captureFailed is how far a failed FetchGateway got: a *FetchError names the
// selected Pod, any other error failed the selection.
func captureFailed(err error) dumpResult {
	r := dumpResult{stage: stageSelect, err: err}
	var fe *proxydump.FetchError
	if errors.As(err, &fe) {
		r.stage, r.target = stageFetch, fe.Pod
	}
	return r
}

func proxyDump(cmd *cobra.Command, f *Flags, hooks proxyTestHooks, name, pod string) error {
	rc, ns, err := f.RESTConfig()
	if err != nil {
		return &ExitError{Code: 2, Err: err}
	}
	if ns == "" {
		return &ExitError{Code: 2, Err: errors.New("proxy dump needs a namespace: use -n, not -A")}
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

	capture, err := proxydump.FetchGateway(ctx, c, d, gw, pod, time.Now)
	if err != nil {
		return dumpError(ctx, f, "proxy dump", gw, captureFailed(err))
	}
	defer capture.Body.Close()
	r := dumpResult{capture: capture, stage: stageFetch, target: capture.Pod}
	out := &errWriter{w: cmd.OutOrStdout()}
	if _, err := io.Copy(out, capture.Body); err != nil {
		if out.err != nil {
			r.err = fmt.Errorf("write config dump of %s: %w", r.target, err)
		} else {
			r.err = fmt.Errorf("read config dump from %s: %w", r.target, err)
		}
		return dumpError(ctx, f, "proxy dump", gw, r)
	}
	return nil
}

// errWriter records w's write error, so a failed copy can say which side failed.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

// dumpError maps a failed dump to exit 2 with what to do next (design §3.3, §4.1).
func dumpError(ctx context.Context, f *Flags, cmd string, gw types.NamespacedName, r dumpResult) error {
	err := r.err
	switch {
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		err = fmt.Errorf("could not read the config dump of Gateway %s within --%s %v", gw, flagRequestTimeout, f.RequestTimeout)
	case ctx.Err() != nil:
		err = errors.New("interrupted")
	case errors.Is(err, proxydump.ErrNoReplica):
		err = fmt.Errorf("%w; name a replica with --pod <pod>", err)
	case apierrors.IsForbidden(err) && r.stage == stageFetch:
		err = fmt.Errorf("%w\nport-forward to Pod %s needs create pods/portforward in namespace %s: see the kgwctl-proxy-portforward Role in docs/rbac.yaml",
			err, r.target, gw.Namespace)
	case apierrors.IsForbidden(err):
		err = fmt.Errorf("%w\n%s needs get on Gateways and list on Pods: see the kgwctl-reader ClusterRole in docs/rbac.yaml", err, cmd)
	}
	return &ExitError{Code: 2, Err: err}
}
