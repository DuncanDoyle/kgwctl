package testenv_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// splitDocs splits a multi-document YAML stream.
func splitDocs(b []byte) [][]byte {
	var out [][]byte
	for _, d := range bytes.Split(b, []byte("\n---")) {
		if len(bytes.TrimSpace(d)) > 0 {
			out = append(out, d)
		}
	}
	return out
}

// TestReaderRoleCoversServedKinds fails when a test CRD kind that the bundle
// lists is missing get, list or watch in the kgwctl-reader ClusterRole.
func TestReaderRoleCoversServedKinds(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role *rbacv1.ClusterRole
	for _, d := range splitDocs(raw) {
		var cr rbacv1.ClusterRole
		if yaml.Unmarshal(d, &cr) == nil && cr.Kind == "ClusterRole" && cr.Name == "kgwctl-reader" {
			role = &cr
		}
	}
	if role == nil {
		t.Fatal("ClusterRole kgwctl-reader not found in docs/rbac.yaml")
	}

	// granted[group/plural] holds the verbs the role grants on that resource.
	granted := map[string][]string{}
	for _, r := range role.Rules {
		for _, g := range r.APIGroups {
			if g == "*" {
				t.Errorf("rule uses a * apiGroup; list groups explicitly")
			}
			for _, res := range r.Resources {
				if res == "*" {
					t.Errorf("rule in group %q uses a * resource; list resources explicitly", g)
				}
				granted[g+"/"+res] = append(granted[g+"/"+res], r.Verbs...)
			}
		}
	}

	// The bundle lists the Gateway API groups whole and kinds in the kgateway category.
	listed := map[string]bool{"gateway.networking.k8s.io": true, "gateway.networking.x-k8s.io": true}
	dirs := []string{"gateway-api", "kgateway"}
	checked := 0
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join("testdata", "crds", dir), func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(p, ".yaml") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, d := range splitDocs(b) {
				var crd apiextv1.CustomResourceDefinition
				if yaml.Unmarshal(d, &crd) != nil || crd.Kind != "CustomResourceDefinition" {
					continue
				}
				group, plural := crd.Spec.Group, crd.Spec.Names.Plural
				if !listed[group] && !slices.Contains(crd.Spec.Names.Categories, "kgateway") {
					continue
				}
				// The bundle collects namespaced kinds and GatewayClass only.
				if crd.Spec.Scope != apiextv1.NamespaceScoped && crd.Spec.Names.Kind != "GatewayClass" {
					continue
				}
				if !slices.ContainsFunc(crd.Spec.Versions, func(v apiextv1.CustomResourceDefinitionVersion) bool { return v.Served }) {
					continue
				}
				checked++
				for _, verb := range []string{"get", "list", "watch"} {
					if !slices.Contains(granted[group+"/"+plural], verb) {
						t.Errorf("kgwctl-reader lacks %s on %s/%s (from %s); update docs/rbac.yaml", verb, group, plural, p)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no CRDs checked; testdata path is wrong")
	}
}
