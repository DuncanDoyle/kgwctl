package cli_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/proxystats"
	"github.com/spf13/cobra"

	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

func TestCLIName(t *testing.T) {
	root := cli.NewRootCommand(cli.WithName("vendorctl"))
	if root.Use != "vendorctl" {
		t.Fatalf("Use = %q, want vendorctl", root.Use)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"completion", "bash"})
	if code := cli.Execute(root); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "vendorctl") || strings.Contains(out.String(), "kgwctl") {
		t.Error("completion script does not use the configured name")
	}
}

func TestDefaults(t *testing.T) {
	if got := cli.NewRootCommand().Use; got != "kgwctl" {
		t.Errorf("default name = %q, want kgwctl", got)
	}
}

func TestWithCommands(t *testing.T) {
	var ran bool
	extra := &cobra.Command{Use: "extra", Run: func(*cobra.Command, []string) { ran = true }}
	root := cli.NewRootCommand(cli.WithCommands(extra))
	root.SetArgs([]string{"extra"})
	if code := cli.Execute(root); code != 0 || !ran {
		t.Errorf("exit %d, ran %v", code, ran)
	}
}

type collector struct{}

func (collector) Name() string                                { return "probe" }
func (collector) Start(context.Context, extension.Deps) error { return nil }

// hints is a command hints type other than the default registry's.
type hints struct{ diagnose.Commands }

// denyAll is an authorizer other than the default registry's.
type denyAll struct{}

func (denyAll) Authorize(*http.Request, objects.Ref) bool { return false }

func TestIgnoredCapabilitiesLogOneDebugLineEach(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	// check runs collectors, the proxy stats source and command hints, so none is ignored.
	reg := extension.New(extension.WithCollectors(collector{}), extension.WithProxyStatsSource(proxystats.Kube{}),
		extension.WithCommands(hints{}), extension.WithAuthorizer(denyAll{}))
	cli.NewRootCommand(cli.WithRegistry(reg))
	if n := strings.Count(buf.String(), "ignored registry capability"); n != 1 || !strings.Contains(buf.String(), "capability=authorizer") {
		t.Errorf("got %d debug lines, want 1 for the authorizer:\n%s", n, buf.String())
	}
	buf.Reset()
	cli.NewRootCommand()
	if buf.Len() != 0 {
		t.Errorf("default registry logged: %s", buf.String())
	}
}
