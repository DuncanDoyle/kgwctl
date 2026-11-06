// Package cli is the extension API of the CLI: a vendor's main builds its own
// root command with its own registry and commands (design §7).
package cli

import (
	"log/slog"
	"reflect"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/internal/app"
)

// BuildInfo identifies a build; main sets it through ldflags.
type BuildInfo = app.BuildInfo

type options struct {
	name     string
	registry *extension.Registry
	commands []*cobra.Command
	build    BuildInfo
}

// Option configures NewRootCommand.
type Option func(*options)

// WithName sets the binary name used in help and completions.
func WithName(name string) Option { return func(o *options) { o.name = name } }

// WithRegistry sets the registry whose kinds, detectors and checks reach the commands.
func WithRegistry(r *extension.Registry) Option { return func(o *options) { o.registry = r } }

// WithCommands adds top-level commands.
func WithCommands(c ...*cobra.Command) Option {
	return func(o *options) { o.commands = append(o.commands, c...) }
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
	return app.NewRoot(app.Config{Name: o.name, Registry: o.registry, Commands: o.commands, Build: o.build})
}

// Execute runs root and returns the exit code: 0, or 1 after "Error: <msg>" on stderr.
// It replaces klog's logger process-wide, so call it before the host starts any client-go goroutine.
func Execute(root *cobra.Command) int { return app.Execute(root) }

// logIgnored logs each registry capability the CLI does not use: the
// long-running collectors and the UI-only authorizer, capabilities and
// command hints.
func logIgnored(r *extension.Registry) {
	ignored := func(name string) { slog.Debug("ignored registry capability", "capability", name) }
	if len(r.Collectors()) > 0 {
		ignored("collectors")
	}
	if r.ProxyStats != nil {
		ignored("proxy stats source")
	}
	// The default registry sets an authorizer and command hints; only a different type counts.
	def := extension.New()
	if reflect.TypeOf(r.Authorizer) != reflect.TypeOf(def.Authorizer) {
		ignored("authorizer")
	}
	if reflect.TypeOf(r.Commands) != reflect.TypeOf(def.Commands) {
		ignored("commands")
	}
	if len(r.CapabilityStates()) > 0 {
		ignored("capabilities")
	}
}
