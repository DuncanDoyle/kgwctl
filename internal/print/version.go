package print

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// VersionText writes v in the layout of design §3.3. An unknown value prints
// as "unknown (<reason>)", never as absent.
func VersionText(w io.Writer, v view.VersionView) error {
	b := &strings.Builder{}
	fmt.Fprintf(b, "Client:  %s\n", client(v.Client))
	// A server section always has a CRD line; --client has none.
	server := v.GatewayAPICRDs != (view.CRDVersion{})
	if !server {
		_, err := io.WriteString(w, b.String())
		return err
	}
	switch {
	case len(v.Installs) == 0 && v.InstallsUnknown != "":
		fmt.Fprintf(b, "Install: unknown (%s)\n", v.InstallsUnknown)
	case len(v.Installs) == 0:
		b.WriteString("Install: none detected\n")
	}
	for _, in := range v.Installs {
		writeInstall(b, in)
	}
	fmt.Fprintf(b, "Gateway API CRDs: %s\n", crds(v.GatewayAPICRDs))
	_, err := io.WriteString(w, b.String())
	return err
}

func client(c view.BuildInfo) string {
	s := strings.TrimSpace(c.Name + " " + c.Version)
	var meta []string
	for _, m := range []string{c.Commit, c.GoVersion} {
		if m != "" {
			meta = append(meta, m)
		}
	}
	if len(meta) > 0 {
		s += " (" + strings.Join(meta, ", ") + ")"
	}
	return s
}

func writeInstall(b *strings.Builder, in view.InstallVersion) {
	fmt.Fprintf(b, "Install: %s\n", in.Display)
	fmt.Fprintf(b, "  Controller: %s%s\n", version(in.Controller), replicas(in))
	switch {
	case in.ProxiesUnknown != "":
		fmt.Fprintf(b, "  Proxies: unknown (%s)\n", in.ProxiesUnknown)
		return
	case len(in.Proxies) == 0:
		b.WriteString("  Proxies: none\n")
		return
	}
	b.WriteString("  Proxies:\n")
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	for _, p := range in.Proxies {
		if p.Unknown != "" {
			fmt.Fprintf(tw, "    %s\tunknown (%s)\n", p.Gateway, p.Unknown)
			continue
		}
		if p.Pods == nil || *p.Pods == 0 {
			fmt.Fprintf(tw, "    %s\tno pods\n", p.Gateway)
			continue
		}
		line := fmt.Sprintf("    %s\t%s\t%s", p.Gateway, version(p.Version), pods(*p.Pods))
		if p.DiffersFromController {
			line += "\tdiffers from controller"
		}
		fmt.Fprintln(tw, line)
	}
	_ = tw.Flush() // writes to a strings.Builder, which never fails
}

// replicas is " (1/2 replicas)", or what is known of the two counts.
func replicas(in view.InstallVersion) string {
	switch {
	case in.Replicas != nil && in.Ready != nil:
		return fmt.Sprintf(" (%d/%d replicas)", *in.Ready, *in.Replicas)
	case in.Replicas != nil:
		return fmt.Sprintf(" (%d desired, ready unknown (%s))", *in.Replicas, in.ReadyUnknown)
	case in.Ready != nil:
		return fmt.Sprintf(" (%d ready, desired unknown)", *in.Ready)
	}
	return " (replicas unknown (" + in.ReadyUnknown + "))"
}

func crds(c view.CRDVersion) string {
	if c.Unknown != "" {
		return "unknown (" + c.Unknown + ")"
	}
	if c.Channel == "" {
		return c.BundleVersion
	}
	return c.BundleVersion + " (" + c.Channel + ")"
}

// version prints a library version with a leading "v" when it starts with a digit.
func version(s string) string {
	switch {
	case s == "":
		return "unknown"
	case unicode.IsDigit(rune(s[0])):
		return "v" + s
	}
	return s
}

func pods(n int) string {
	if n == 1 {
		return "1 pod"
	}
	return fmt.Sprintf("%d pods", n)
}
