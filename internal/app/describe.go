package app

import (
	"fmt"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// newDescribeCmd builds the describe command (design §3.2).
func newDescribeCmd(reg *extension.Registry, now func() time.Time) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "describe <kind> <name>",
		Short: "Show one object in full: spec, status blocks and relations",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != print.FormatText && output != print.FormatJSON && output != print.FormatYAML {
				return fmt.Errorf("invalid --output %q: want text, json or yaml", output)
			}
			f, err := FlagsFrom(cmd)
			if err != nil {
				return err
			}
			res, targets, ns, err := loadGet(cmd, f, reg, args[0])
			if err != nil {
				return err
			}
			if len(targets) != 1 {
				return fmt.Errorf("describe takes one kind, got %d", len(targets))
			}
			// Hiding never applies to a named object (Ruling R29), so the filter's warnings are not shown.
			filter, _ := view.NewFilter(res, view.Scope{Namespace: ns, AllNamespaces: f.AllNamespaces,
				Install: f.Install, AllControllers: f.AllControllers})
			v, err := view.Describe(res, filter, targets[0], ns, args[1], now())
			if err != nil {
				return err
			}
			for _, in := range v.Incomplete {
				warn(cmd.ErrOrStderr(), in)
			}
			if output == print.FormatText {
				return print.DescribeText(cmd.OutOrStdout(), v)
			}
			return print.Envelope(cmd.OutOrStdout(), output, "Describe", v)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", print.FormatText, "Output format: text, json or yaml")
	return cmd
}
