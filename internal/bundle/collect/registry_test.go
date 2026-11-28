package collect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TestRegistryCoversCollectedKinds fails when a test CRD kind the resources
// collector lists (by its own listsWhole rule) has no descriptor in the
// bundle's effective registry with a served version, a category and the CRD's
// scope, so scope rules would not see it (design §3.2). Add it to unregistered.
func TestRegistryCoversCollectedKinds(t *testing.T) {
	for _, m := range uncovered(t, Registry(extension.New())) {
		t.Errorf("no effective registry descriptor with a category serves %s", m)
	}
}

// TestRegistryGuardSeesShadowedDescriptor: a registry descriptor with no
// served version shadows the bundle's fallback, so the guard reports the kind.
func TestRegistryGuardSeesShadowedDescriptor(t *testing.T) {
	own := kinds.Descriptor{Group: gatewayGroup, Kind: "UDPRoute", Resource: "udproutes", Versions: []string{"v9"},
		Namespaced: true, Category: kinds.CategoryRoute}
	missing := uncovered(t, Registry(extension.New(extension.WithKinds(own))))
	if !slices.ContainsFunc(missing, func(m string) bool { return strings.HasPrefix(m, gatewayGroup+"/UDPRoute ") }) {
		t.Errorf("uncovered %v, want %s/UDPRoute", missing, gatewayGroup)
	}
}

// uncovered returns the test CRD kinds the resources collector lists that
// reg's effective descriptors, deduplicated as runtime resolution does, miss.
func uncovered(t *testing.T, reg *extension.Registry) []string {
	t.Helper()
	descs := kinds.Dedupe(reg.Kinds)
	var missing []string
	checked := 0
	err := filepath.WalkDir(filepath.Join("..", "..", "testenv", "testdata", "crds"), func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, d := range bytes.Split(b, []byte("\n---")) {
			var crd apiextv1.CustomResourceDefinition
			if yaml.Unmarshal(d, &crd) != nil || crd.Kind != "CustomResourceDefinition" {
				continue
			}
			s := crd.Spec
			if !listsWhole(s.Group, s.Names.Categories) {
				continue
			}
			// The bundle collects namespaced kinds and GatewayClass only.
			namespaced := s.Scope == apiextv1.NamespaceScoped
			if !namespaced && s.Names.Kind != gatewayClassGK.Kind {
				continue
			}
			var served []string
			for _, v := range s.Versions {
				if v.Served {
					served = append(served, v.Name)
				}
			}
			if len(served) == 0 {
				continue
			}
			checked++
			if !slices.ContainsFunc(descs, func(k kinds.Descriptor) bool {
				return k.Group == s.Group && k.Kind == s.Names.Kind && k.Resource == s.Names.Plural &&
					k.Category != "" && k.Namespaced == namespaced &&
					slices.ContainsFunc(k.Versions, func(v string) bool { return slices.Contains(served, v) })
			}) {
				missing = append(missing, fmt.Sprintf("%s/%s (%s, versions %v)", s.Group, s.Names.Kind, p, served))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no CRDs checked; testdata path is wrong")
	}
	return missing
}

// TestRegistryKeepsOwnDescriptors: a descriptor the registry already has
// wins over the bundle's, and the input registry is not changed.
func TestRegistryKeepsOwnDescriptors(t *testing.T) {
	own := kinds.Descriptor{Group: gatewayGroup, Kind: "UDPRoute", Resource: "udproutes", Versions: []string{"v9"},
		Namespaced: true, Category: kinds.CategoryRoute}
	in := extension.New(extension.WithKinds(own))
	n := len(in.Kinds)
	out := Registry(in)
	if len(in.Kinds) != n {
		t.Errorf("input registry changed: %d kinds, want %d", len(in.Kinds), n)
	}
	var got []kinds.Descriptor
	for _, d := range kinds.Dedupe(out.Kinds) {
		if d.Kind == "UDPRoute" {
			got = append(got, d)
		}
	}
	if len(got) != 1 || !slices.Equal(got[0].Versions, own.Versions) {
		t.Errorf("UDPRoute descriptors %+v, want only the registry's own", got)
	}
}
