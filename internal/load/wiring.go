// This file is the only copy of the cache and engine wiring in kgateway-ui's
// pkg/app/app.go (Build, lines 205-251 at 5737a14). It is replaced by the
// library's exported config builder once DuncanDoyle/kgateway-ui#13 lands.

package load

import (
	"log/slog"

	"github.com/DuncanDoyle/kgateway-ui/pkg/cache"
	"github.com/DuncanDoyle/kgateway-ui/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/kgateway-ui/pkg/engine"
	"github.com/DuncanDoyle/kgateway-ui/pkg/extension"
	"github.com/DuncanDoyle/kgateway-ui/pkg/install"
	"github.com/DuncanDoyle/kgateway-ui/pkg/kinds"
	"github.com/DuncanDoyle/kgateway-ui/pkg/platform"
	"github.com/DuncanDoyle/kgateway-ui/pkg/scopeverify"
	"k8s.io/apimachinery/pkg/labels"
)

// cacheConfig builds the cache config as pkg/app does, with no change signal.
// Optional watches run only when needs asks for them; Events never run.
func cacheConfig(c Clients, reg *extension.Registry, served []kinds.Served, needs Needs) cache.Config {
	var podSel, depSel, proxySel []labels.Selector
	for _, d := range reg.Detectors {
		podSel = append(podSel, d.ControllerPodSelector()) // Pods may lack the Deployment's component label
		depSel = append(depSel, d.ControllerSelector(), d.ProxySelector())
		if pd, ok := d.(install.ProxyPodDetector); ok && needs.ProxyPods {
			proxySel = append(proxySel, pd.ProxyPodSelector())
		}
	}
	return cache.Config{Dynamic: c.Dynamic, Kube: c.Kube, Served: served, PodSelectors: podSel, DeploymentSelectors: depSel,
		ProxyPodSelectors: proxySel, CRDs: needs.CRDs, Metadata: c.Metadata}
}

// engineConfig builds the engine config as pkg/app does, over one fixed
// snapshot, with static metrics and none of the Run loop's fields. A nil log
// discards the engine's logs.
func engineConfig(reg *extension.Registry, served []kinds.Served, snap *cache.Snapshot, log *slog.Logger) engine.Config {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	verifier := reg.Verifier
	if verifier == nil {
		// Metrics are never present, so the persistence window is never reached.
		verifier = scopeverify.NewManagedCountVerifier(0)
	}
	return engine.Config{Snapshots: fixedSnapshot{snap}, Served: served, Detectors: reg.Detectors, Scope: reg.Scope, Verifier: verifier,
		Metrics: noMetrics{}, Checks: reg.Checks, Platform: platform.Kubernetes(), ProxyMode: reg.ProxyMode,
		RulesFor: reg.RulesFor, Process: reg.Process, Logger: log}
}

// noMetrics is a static ctrlmetrics.Source with no data for any install.
type noMetrics struct{}

// Get implements ctrlmetrics.Source.
func (noMetrics) Get(string) (ctrlmetrics.Result, bool) { return ctrlmetrics.Result{}, false }

// fixedSnapshot hands Compute the snapshot Load classified its inputs from.
type fixedSnapshot struct{ s *cache.Snapshot }

// Snapshot implements cache.Snapshotter.
func (f fixedSnapshot) Snapshot() *cache.Snapshot { return f.s }
