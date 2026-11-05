// Package load reads a cluster once for a command: clients, discovery, cache
// and engine (design §4).
package load

import (
	"context"
	"fmt"

	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
)

// Clients are the Kubernetes clients of one command. All share one HTTP
// client whose requests are bound to the command context.
type Clients struct {
	Discovery discovery.DiscoveryInterface
	Dynamic   dynamic.Interface
	Kube      kubernetes.Interface
	Metadata  metadata.Interface
}

// NewClients builds the four clients from a copy of cfg, with every request
// bound to ctx through BindContext. It makes no requests.
func NewClients(ctx context.Context, cfg *rest.Config) (Clients, error) {
	cfg = rest.CopyConfig(cfg)
	cfg.Wrap(BindContext(ctx))
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return Clients{}, fmt.Errorf("build HTTP client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfigAndClient(cfg, hc)
	if err != nil {
		return Clients{}, fmt.Errorf("build discovery client: %w", err)
	}
	dyn, err := dynamic.NewForConfigAndClient(cfg, hc)
	if err != nil {
		return Clients{}, fmt.Errorf("build dynamic client: %w", err)
	}
	kube, err := kubernetes.NewForConfigAndClient(cfg, hc)
	if err != nil {
		return Clients{}, fmt.Errorf("build kube client: %w", err)
	}
	meta, err := metadata.NewForConfigAndClient(cfg, hc)
	if err != nil {
		return Clients{}, fmt.Errorf("build metadata client: %w", err)
	}
	return Clients{Discovery: disc, Dynamic: dyn, Kube: kube, Metadata: meta}, nil
}

// Discovery adapts c to kinds.DiscoveryClient with requests that run under
// ctx, so Retry-After waits and client-side throttling end at the deadline.
// client-go's own ServerResourcesForGroupVersion uses context.TODO().
func Discovery(ctx context.Context, c discovery.DiscoveryInterface) kinds.DiscoveryClient {
	return discoveryAdapter{ctx: ctx, c: c}
}

type discoveryAdapter struct {
	ctx context.Context
	c   discovery.DiscoveryInterface
}

// ServerResourcesForGroupVersion implements kinds.DiscoveryClient with the
// same request and decoding as client-go's.
func (d discoveryAdapter) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	path := "/apis/" + gv
	if gv == "v1" {
		path = "/api/v1"
	}
	l := &metav1.APIResourceList{GroupVersion: gv}
	if err := d.c.RESTClient().Get().AbsPath(path).Do(d.ctx).Into(l); err != nil {
		// client-go treats a missing core group as empty; so does this adapter.
		if gv == "v1" && apierrors.IsNotFound(err) {
			return l, nil
		}
		return nil, err
	}
	return l, nil
}
