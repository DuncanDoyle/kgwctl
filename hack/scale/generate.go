// Command generate writes the §6.4 scale fixture as multi-document YAML to
// stdout: 1,000 Namespaces, 500 Gateways and 5,000 HTTPRoutes, with no status.
//
// The Gateways use GatewayClass kgateway-scale, which kgateway reconciles but
// whose GatewayParameters are self-managed, so no proxy Pods are deployed.
package main

import (
	"bufio"
	"fmt"
	"os"
)

const (
	namespaces       = 1000
	gateways         = 500
	routes           = 5000
	routesPerNS      = routes / namespaces
	label            = "kgwctl-scale: \"true\""
	gatewayClass     = "kgateway-scale"
	paramsNamespace  = "scale-0000"
	paramsName       = "self-managed"
	kgatewayCtrlName = "kgateway.dev/kgateway"
)

func ns(i int) string { return fmt.Sprintf("scale-%04d", i) }

func main() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	doc := func(format string, args ...any) {
		fmt.Fprintf(w, "---\n"+format, args...)
	}

	for i := range namespaces {
		doc("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels:\n    %s\n", ns(i), label)
	}
	doc(`apiVersion: gateway.kgateway.dev/v1alpha1
kind: GatewayParameters
metadata:
  name: %s
  namespace: %s
spec:
  selfManaged: {}
`, paramsName, paramsNamespace)
	doc(`apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: %s
  labels:
    %s
spec:
  controllerName: %s
  parametersRef:
    group: gateway.kgateway.dev
    kind: GatewayParameters
    name: %s
    namespace: %s
`, gatewayClass, label, kgatewayCtrlName, paramsName, paramsNamespace)
	// One Gateway in each of the first 500 namespaces, accepting routes from all.
	for i := range gateways {
		doc(`apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: gw
  namespace: %s
spec:
  gatewayClassName: %s
  listeners:
    - name: http
      protocol: HTTP
      port: 8080
      allowedRoutes:
        namespaces:
          from: All
`, ns(i), gatewayClass)
	}
	// Five routes per namespace; route r in namespace i attaches to the Gateway in namespace i mod 500.
	for i := range namespaces {
		for r := range routesPerNS {
			doc(`apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: route-%d
  namespace: %s
spec:
  parentRefs:
    - name: gw
      namespace: %s
  hostnames:
    - r%d.%s.example.com
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
`, r, ns(i), ns(i%gateways), r, ns(i))
		}
	}
}
