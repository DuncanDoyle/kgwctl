package collect_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/bundle/collect"
)

// TestRegistryCoversCollectedKinds fails when a test CRD kind the resources
// collector lists has no descriptor in the bundle's registry, so scope
// rules would not see it (design §3.2). Add it to the collector's kinds.
func TestRegistryCoversCollectedKinds(t *testing.T) {
	reg := collect.Registry(extension.New())
	listed := map[string]bool{"gateway.networking.k8s.io": true, "gateway.networking.x-k8s.io": true}
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
			if !listed[s.Group] && !slices.Contains(s.Names.Categories, "kgateway") {
				continue
			}
			// The bundle collects namespaced kinds and GatewayClass only.
			if s.Scope != apiextv1.NamespaceScoped && s.Names.Kind != "GatewayClass" {
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
			if !slices.ContainsFunc(reg.Kinds, func(k kinds.Descriptor) bool {
				return k.Group == s.Group && k.Kind == s.Names.Kind && k.Resource == s.Names.Plural &&
					slices.ContainsFunc(k.Versions, func(v string) bool { return slices.Contains(served, v) })
			}) {
				t.Errorf("no registry descriptor serves %s/%s (%s, versions %v)", s.Group, s.Names.Kind, p, served)
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
}

// TestRegistryKeepsOwnDescriptors: a descriptor the registry already has
// wins over the bundle's, and the input registry is not changed.
func TestRegistryKeepsOwnDescriptors(t *testing.T) {
	own := kinds.Descriptor{Group: "gateway.networking.k8s.io", Kind: "UDPRoute", Resource: "udproutes", Versions: []string{"v9"},
		Namespaced: true, Category: kinds.CategoryRoute}
	in := extension.New(extension.WithKinds(own))
	n := len(in.Kinds)
	out := collect.Registry(in)
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
