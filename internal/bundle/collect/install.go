package collect

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

var (
	deploymentsGVR    = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	gatewayClassesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
)

type installCollector struct{}

// Install returns the collector of each selected install's controller
// Deployment, at install/<namespace>/<deployment>.yaml, and of the
// GatewayClasses Env.Keep accepts, at install/_cluster/<name>.yaml.
// Left-out GatewayClasses are counted by the resources collector, not here.
func Install() pb.Collector { return installCollector{} }

// Name implements pb.Collector.
func (installCollector) Name() string { return "install" }

// Collect implements pb.Collector.
func (installCollector) Collect(ctx context.Context, env pb.Env, s pb.Sink) error {
	for _, in := range env.Scope.Installs {
		src := pb.Source{Namespace: in.Namespace, Operation: pb.OpList}
		f, err := pb.File(in.Deployment, pb.YAML)
		if err != nil {
			s.Error(pb.Path{pb.InstallDir}, src, err)
			continue
		}
		p := pb.Path{pb.InstallDir, in.Namespace, f}
		rctx, cancel := requestContext(ctx, env.RequestTimeout)
		u, err := env.Dynamic.Resource(deploymentsGVR).Namespace(in.Namespace).Get(rctx, in.Deployment, metav1.GetOptions{})
		cancel()
		if err != nil {
			s.Error(p, src, err)
			continue
		}
		u.SetGroupVersionKind(deploymentsGVR.GroupVersion().WithKind("Deployment"))
		if err := sealed(s.Object(p, u, src)); err != nil {
			return err
		}
	}
	gcs, err := list(ctx, env, gatewayClassesGVR, "")
	if err != nil {
		s.Error(pb.Path{pb.InstallDir, pb.Cluster}, pb.Source{Operation: pb.OpList}, err)
		return nil
	}
	for i := range gcs {
		u := &gcs[i]
		u.SetGroupVersionKind(gatewayClassesGVR.GroupVersion().WithKind("GatewayClass"))
		if env.Keep != nil && env.Keep(u) != pb.ExcludeNone {
			continue
		}
		src := pb.Source{Operation: pb.OpList}
		f, err := pb.File(u.GetName(), pb.YAML)
		if err != nil {
			s.Error(pb.Path{pb.InstallDir, pb.Cluster}, src, err)
			continue
		}
		if err := sealed(s.Object(pb.Path{pb.InstallDir, pb.Cluster, f}, u, src)); err != nil {
			return err
		}
	}
	return nil
}
