// Package app builds the root command and runs it (design §3, §8).
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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
func FlagsFrom(cmd *cobra.Command) *Flags {
	fs := cmd.Flags()
	f := &Flags{}
	f.Kubeconfig, _ = fs.GetString(flagKubeconfig)
	f.ContextName, _ = fs.GetString(flagContext)
	f.Namespace, _ = fs.GetString(flagNamespace)
	f.AllNamespaces, _ = fs.GetBool(flagAllNamespaces)
	f.RequestTimeout, _ = fs.GetDuration(flagRequestTimeout)
	f.Install, _ = fs.GetString(flagInstall)
	f.AllControllers, _ = fs.GetBool(flagAllControllers)
	return f
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

// Context returns parent bounded by --request-timeout (design §4.4).
func (f *Flags) Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, f.RequestTimeout)
}

// NewRoot builds the root command with the global flags, completion and
// cfg.Commands.
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
	root := &cobra.Command{
		Use:           cfg.Name,
		Short:         "Inspect kgateway installs and the Gateway API objects they serve",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(cfg.Out)
	root.SetErr(cfg.Err)
	(&Flags{}).Bind(root.PersistentFlags())
	root.AddCommand(cfg.Commands...)
	return root
}

// Execute runs root until it returns or the process is interrupted. An error
// prints as "Error: <msg>" on stderr and gives 1; success gives 0.
func Execute(root *cobra.Command) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(root.ErrOrStderr(), "Error: %v\n", err)
		return 1
	}
	return 0
}
