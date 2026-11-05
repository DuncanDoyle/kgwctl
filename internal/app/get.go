package app

import (
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/kgwctl/internal/load"
	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/resolve"
	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// newGetCmd builds the get command (design §3.1).
func newGetCmd(reg *extension.Registry, now func() time.Time) *cobra.Command {
	var output, selector string
	cmd := &cobra.Command{
		Use:   "get <kind>[,<kind>...] [name]",
		Short: "List Gateway API and kgateway objects with their status",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch output {
			case print.FormatTable, print.FormatWide, print.FormatYAML, print.FormatJSON:
			default:
				return fmt.Errorf("invalid --output %q: want table, wide, yaml or json", output)
			}
			sel, err := labels.Parse(selector)
			if err != nil {
				return fmt.Errorf("invalid --selector %q: %w", selector, err)
			}
			var name string
			if len(args) == 2 {
				name = args[1]
			}
			f, err := FlagsFrom(cmd)
			if err != nil {
				return err
			}
			res, targets, ns, err := loadGet(cmd, f, reg, args[0])
			if err != nil {
				return err
			}
			filter, warns := view.NewFilter(res, view.Scope{Namespace: ns, AllNamespaces: f.AllNamespaces,
				Install: f.Install, AllControllers: f.AllControllers, Selector: sel})
			wide := output == print.FormatWide
			tables, objs, err := view.GetTables(res, filter, targets, wide, name, now())
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			for _, w := range warns {
				fmt.Fprintf(stderr, "Warning: %s\n", w)
			}
			if output == print.FormatYAML || output == print.FormatJSON {
				return print.RawList(cmd.OutOrStdout(), output, objs)
			}
			if down := filter.IndexUnavailable(); down != "" && indexed(tables) {
				fmt.Fprintf(stderr, "Warning: relations unknown (%s)\n", down)
			}
			wrote, err := print.Tables(cmd.OutOrStdout(), tables)
			if err == nil && !wrote {
				_, err = io.WriteString(stderr, print.NoResources(ns)+"\n")
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", print.FormatTable, "Output format: table, wide, yaml or json")
	cmd.Flags().StringVarP(&selector, "selector", "l", "", "Label selector to filter on")
	return cmd
}

// loadGet loads the cluster for get, resolving kindArg against the served
// kinds before the wait so the loader waits for them (§4.4).
func loadGet(cmd *cobra.Command, f *Flags, reg *extension.Registry, kindArg string) (*load.Result, []resolve.Target, string, error) {
	rc, ns, err := f.RESTConfig()
	if err != nil {
		return nil, nil, "", err
	}
	ctx, cancel := f.Context(cmd.Context())
	defer cancel()
	c, err := load.NewClients(ctx, rc)
	if err != nil {
		return nil, nil, "", err
	}
	var targets []resolve.Target
	pick := func(served []kinds.Served) ([]schema.GroupVersionResource, error) {
		var err error
		if targets, err = resolve.Kinds(served, reg.Kinds, kindArg); err != nil {
			return nil, err
		}
		var gvrs []schema.GroupVersionResource
		for _, t := range targets {
			for _, k := range t.Kinds {
				gvrs = append(gvrs, k.GVR())
			}
		}
		return gvrs, nil
	}
	res, err := load.Load(ctx, c, reg, load.Needs{Resolve: pick, Logger: engineLogger(cmd.ErrOrStderr())})
	if err != nil {
		return nil, nil, "", err
	}
	return res, targets, ns, nil
}

// indexed reports whether a table with rows shows cells derived from the explore index.
func indexed(ts []view.Table) bool {
	return slices.ContainsFunc(ts, func(t view.Table) bool { return t.Indexed && len(t.Rows) > 0 })
}
