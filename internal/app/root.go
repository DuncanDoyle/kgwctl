// Package app builds the root command and runs it (design §3, §8).
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// BuildInfo identifies the build.
type BuildInfo = view.BuildInfo

// Config configures NewRoot.
type Config struct {
	// Name is the binary name in help and completions.
	Name string
	// Registry holds the kinds, detectors and checks; nil means extension.New().
	Registry *extension.Registry
	// Commands are added as top-level commands.
	Commands []*cobra.Command
	Build    BuildInfo
	// Out and Err default to os.Stdout and os.Stderr.
	Out, Err io.Writer
}

// Flags are the global cluster flags (design §6.1).
type Flags struct {
	Kubeconfig     string
	ContextName    string
	Namespace      string
	AllNamespaces  bool
	RequestTimeout time.Duration
	Install        string
	AllControllers bool
}

// Flag names, shared by Bind and FlagsFrom.
const (
	flagKubeconfig     = "kubeconfig"
	flagContext        = "context"
	flagNamespace      = "namespace"
	flagAllNamespaces  = "all-namespaces"
	flagRequestTimeout = "request-timeout"
	flagInstall        = "install"
	flagAllControllers = "all-controllers"
)

// DefaultRequestTimeout is the default of --request-timeout.
const DefaultRequestTimeout = 30 * time.Second

// Bind registers the global flags on fs.
func (f *Flags) Bind(fs *pflag.FlagSet) {
	fs.StringVar(&f.Kubeconfig, flagKubeconfig, "", "Path to the kubeconfig file")
	fs.StringVar(&f.ContextName, flagContext, "", "Kubeconfig context to use")
	fs.StringVarP(&f.Namespace, flagNamespace, "n", "", "Namespace scope; defaults to the context's namespace")
	fs.BoolVarP(&f.AllNamespaces, flagAllNamespaces, "A", false, "List across all namespaces")
	fs.DurationVar(&f.RequestTimeout, flagRequestTimeout, DefaultRequestTimeout, "Deadline for all cluster I/O of the command")
	fs.StringVar(&f.Install, flagInstall, "", "Select the install `<ns>/<name>`")
	fs.BoolVar(&f.AllControllers, flagAllControllers, false, "Show objects of all controllers, not only the selected install's")
}

// FlagsFrom reads the global flags as parsed for cmd or one of its parents.
// It fails if they are not registered (cmd is outside NewRoot) or if
// --request-timeout is negative.
func FlagsFrom(cmd *cobra.Command) (*Flags, error) {
	fs := cmd.Flags()
	f := &Flags{}
	var errs []error
	get := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var err error
	f.Kubeconfig, err = fs.GetString(flagKubeconfig)
	get(err)
	f.ContextName, err = fs.GetString(flagContext)
	get(err)
	f.Namespace, err = fs.GetString(flagNamespace)
	get(err)
	f.AllNamespaces, err = fs.GetBool(flagAllNamespaces)
	get(err)
	f.RequestTimeout, err = fs.GetDuration(flagRequestTimeout)
	get(err)
	f.Install, err = fs.GetString(flagInstall)
	get(err)
	f.AllControllers, err = fs.GetBool(flagAllControllers)
	get(err)
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("global flags not registered on %q: %w", cmd.Name(), err)
	}
	if f.RequestTimeout < 0 {
		return nil, fmt.Errorf("invalid --%s %v: must not be negative", flagRequestTimeout, f.RequestTimeout)
	}
	return f, nil
}

// RESTConfig loads the kubeconfig with the standard clientcmd rules and
// returns the config and the namespace to use: -n, else the context's, and ""
// for -A.
func (f *Flags) RESTConfig() (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = f.Kubeconfig
	overrides := &clientcmd.ConfigOverrides{CurrentContext: f.ContextName}
	if f.Namespace != "" {
		overrides.Context.Namespace = f.Namespace
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolve namespace: %w", err)
	}
	if f.AllNamespaces {
		ns = ""
	}
	return cfg, ns, nil
}

// Context returns parent bounded by --request-timeout (design §4.4); 0 means
// no deadline.
func (f *Flags) Context(parent context.Context) (context.Context, context.CancelFunc) {
	if f.RequestTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, f.RequestTimeout)
}

// NewRoot builds the root command with the global flags, version,
// completion and cfg.Commands.
func NewRoot(cfg Config) *cobra.Command {
	if cfg.Name == "" {
		cfg.Name = "kgwctl"
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.Err == nil {
		cfg.Err = os.Stderr
	}
	if cfg.Registry == nil {
		cfg.Registry = extension.New()
	}
	if cfg.Build.Name == "" {
		cfg.Build.Name = cfg.Name
	}
	root := &cobra.Command{
		Use:           cfg.Name,
		Short:         "Inspect kgateway installs and the Gateway API objects they serve",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(cfg.Out)
	root.SetErr(cfg.Err)
	(&Flags{}).Bind(root.PersistentFlags())
	root.AddCommand(newVersionCmd(cfg.Registry, cfg.Build))
	root.AddCommand(cfg.Commands...)
	return root
}

// silenceKlog sets klog's logger once: watch goroutines of earlier runs still read it (§4.3).
var silenceKlog sync.Once

// Execute runs root until it returns or the process is interrupted. An error
// prints as "Error: <msg>" on stderr and gives 1; success gives 0. It
// silences client-go's klog output once per process: commands report inputs as warnings.
func Execute(root *cobra.Command) int {
	silenceKlog.Do(func() { klog.SetLogger(logr.Discard()) })
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(root.ErrOrStderr(), "Error: %v\n", err)
		return 1
	}
	return 0
}
