package app

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// newVersionCmd builds the version command (design §3.3).
func newVersionCmd(reg *extension.Registry, build BuildInfo) *cobra.Command {
	var client bool
	var output string
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the client, controller, proxy and Gateway API CRD versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != print.FormatText && output != print.FormatJSON && output != print.FormatYAML {
				return fmt.Errorf("invalid --output %q: want text, json or yaml", output)
			}
			var res *load.Result
			if !client {
				var err error
				if res, err = loadVersion(cmd, reg); err != nil {
					return err
				}
			}
			v := view.Version(build, res)
			for _, in := range v.Incomplete {
				warn(cmd.ErrOrStderr(), in)
			}
			if output == print.FormatText {
				return print.VersionText(cmd.OutOrStdout(), v)
			}
			return print.Envelope(cmd.OutOrStdout(), output, "Version", v)
		},
	}
	cmd.Flags().BoolVar(&client, "client", false, "Print the client version only; never contacts a cluster")
	cmd.Flags().StringVarP(&output, "output", "o", print.FormatText, "Output format: text, json or yaml")
	return cmd
}

// loadVersion loads the cluster the global flags select, waiting for proxy Pods and CRDs too (§4.1).
func loadVersion(cmd *cobra.Command, reg *extension.Registry) (*load.Result, error) {
	f, err := FlagsFrom(cmd)
	if err != nil {
		return nil, err
	}
	rc, _, err := f.RESTConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := f.Context(cmd.Context())
	defer cancel()
	c, err := load.NewClients(ctx, rc)
	if err != nil {
		return nil, err
	}
	return load.Load(ctx, c, reg, load.Needs{ProxyPods: true, CRDs: true, Logger: engineLogger(cmd.ErrOrStderr())})
}

// engineLogger passes the engine's warnings and errors to w and drops the rest.
func engineLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// warn writes one stderr line for an input that is not ok.
func warn(w io.Writer, in load.Input) {
	if in.Reason == "" {
		fmt.Fprintf(w, "Warning: %s: %s\n", in.Resource, in.State)
		return
	}
	fmt.Fprintf(w, "Warning: %s: %s: %s\n", in.Resource, in.State, in.Reason)
}
