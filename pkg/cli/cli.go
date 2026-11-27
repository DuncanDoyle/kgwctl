// Package cli is the extension API of the CLI: a vendor's main builds its own
// root command with its own registry and commands (design §7).
package cli

import (
	"log/slog"
	"reflect"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/internal/app"
	"github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// BuildInfo identifies a build; main sets it through ldflags.
type BuildInfo = app.BuildInfo

type options struct {
	name     string
	registry *extension.Registry
	commands []*cobra.Command
	build    BuildInfo

	collectors []bundle.Collector
	modules    []provenance.Module
}

// Option configures NewRootCommand.
type Option func(*options)

// WithName sets the binary name used in help and completions.
func WithName(name string) Option { return func(o *options) { o.name = name } }

// WithRegistry sets the registry whose kinds, detectors and checks reach the commands.
// A vendor's checks, collectors and kinds belong in its own Snellius pack
// (extension.WithPack). extension.WithDetectors attaches detectors to the
// default kgateway pack, so the installs they claim get the kgateway checks.
func WithRegistry(r *extension.Registry) Option { return func(o *options) { o.registry = r } }

// WithCommands adds top-level commands.
func WithCommands(c ...*cobra.Command) Option {
	return func(o *options) { o.commands = append(o.commands, c...) }
}

// WithBundleCollectors adds collectors to `bundle`, run after the built-in ones.
// An invalid name, or one that repeats a built-in or another added collector, fails `bundle`.
func WithBundleCollectors(c ...bundle.Collector) Option {
	return func(o *options) { o.collectors = append(o.collectors, c...) }
}

// WithProvenanceModules sets the provenance modules `proxy routes` composes, in place of
// the default kgateway module; calls add to one set, and a call with no modules keeps the default.
// Wrap kgateway.New() in a module under your own name, never "kgateway"; two modules with
// one name make `proxy routes` exit 2. Other commands do not use the modules.
func WithProvenanceModules(m ...provenance.Module) Option {
	return func(o *options) { o.modules = append(o.modules, m...) }
}

// WithBuildInfo sets the build information that version reports.
func WithBuildInfo(b BuildInfo) Option { return func(o *options) { o.build = b } }

// NewRootCommand builds the root command. The name defaults to "kgwctl" and
// the registry to extension.New(). Registry capabilities this CLI does not
// support are ignored with one debug log line each.
func NewRootCommand(opts ...Option) *cobra.Command {
	o := options{name: "kgwctl"}
	for _, opt := range opts {
		opt(&o)
	}
	if o.registry == nil {
		o.registry = extension.New()
	}
	logIgnored(o.registry)
	return app.NewRoot(app.Config{Name: o.name, Registry: o.registry, Commands: o.commands, Collectors: o.collectors, Modules: o.modules, Build: o.build})
}

// Execute runs root and returns the exit code: 0, or 1 after "Error: <msg>" on stderr.
// It replaces klog's logger process-wide, so call it before the host starts any client-go goroutine.
func Execute(root *cobra.Command) int { return app.Execute(root) }

// logIgnored logs each registry capability the CLI does not use: the UI-only
// authorizer and capabilities. check uses the collectors, proxy stats source and command hints.
func logIgnored(r *extension.Registry) {
	ignored := func(name string) { slog.Debug("ignored registry capability", "capability", name) }
	// The default registry sets an authorizer; only a different type counts.
	def := extension.New()
	if reflect.TypeOf(r.Authorizer) != reflect.TypeOf(def.Authorizer) {
		ignored("authorizer")
	}
	if len(r.CapabilityStates()) > 0 {
		ignored("capabilities")
	}
}
