// Command vendorctl is a downstream CLI built only on pkg/cli. It registers a
// policy kind, an install detector and a command to show the extension seam.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/DuncanDoyle/kgwctl/pkg/cli"
)

// controllerName is the GatewayClass controller name of the example distribution.
const controllerName = "example.com/gateway-controller"

func main() {
	os.Exit(cli.Execute(newRoot()))
}

// newRoot builds the vendorctl root command; main and the tests share it.
func newRoot() *cobra.Command {
	return cli.NewRootCommand(
		cli.WithName("vendorctl"),
		cli.WithRegistry(newRegistry()),
		cli.WithCommands(&cobra.Command{
			Use:   "hello",
			Short: "Print hello",
			Args:  cobra.NoArgs,
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), "hello") },
		}),
	)
}

// newRegistry adds the ExamplePolicy kind and the example detector to the defaults.
func newRegistry() *extension.Registry {
	return extension.New(
		extension.WithKinds(kinds.Descriptor{
			Group: "policy.example.com", Kind: "ExamplePolicy", Resource: "examplepolicies",
			Versions: []string{"v1"}, Namespaced: true, Category: kinds.CategoryPolicy, DisplayName: "ExamplePolicy",
		}),
		extension.WithDetectors(detector{}),
	)
}

// detector recognises the example controller: Deployments labelled
// app=vendor-controller whose image repository is "vendor-controller".
type detector struct{}

func (detector) Name() string { return "example" }

func (detector) ControllerSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-controller"})
}

func (detector) ControllerPodSelector() labels.Selector { return detector{}.ControllerSelector() }

func (detector) ProxySelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{"app": "vendor-proxy"})
}

func (detector) Detect(d *appsv1.Deployment) (install.Install, bool) {
	for _, c := range d.Spec.Template.Spec.Containers {
		repo := c.Image[strings.LastIndex(c.Image, "/")+1:]
		repo, tag, _ := strings.Cut(repo, ":")
		if repo != "vendor-controller" {
			continue
		}
		version := d.Labels["app.kubernetes.io/version"]
		if version == "" {
			version = strings.TrimPrefix(tag, "v")
		}
		return install.Install{
			Namespace: d.Namespace, Name: d.Name, Edition: "example", Version: version, Container: c.Name,
			ControllerNames: []string{controllerName}, Deployment: d, ProxySelector: detector{}.ProxySelector(),
		}, true
	}
	return install.Install{}, false
}
