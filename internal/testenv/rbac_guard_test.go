package testenv_test

import (
	"bytes"
	"fmt"
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

// servedKind is a test CRD kind the bundle lists, and the file that defines it.
type servedKind struct{ group, plural, src string }

// readerRole returns the kgwctl-reader ClusterRole from docs/rbac.yaml.
func readerRole(t *testing.T) *rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range splitDocs(raw) {
		var cr rbacv1.ClusterRole
		if yaml.Unmarshal(d, &cr) == nil && cr.Kind == "ClusterRole" && cr.Name == "kgwctl-reader" {
			return &cr
		}
	}
	t.Fatal("ClusterRole kgwctl-reader not found in docs/rbac.yaml")
	return nil
}

// servedKinds returns the test CRD kinds the bundle lists.
func servedKinds(t *testing.T) []servedKind {
	t.Helper()
	// The bundle lists the Gateway API groups whole and kinds in the kgateway category.
	listed := map[string]bool{"gateway.networking.k8s.io": true, "gateway.networking.x-k8s.io": true}
	var out []servedKind
	for _, dir := range []string{"gateway-api", "kgateway", "xlistenerset"} {
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
				out = append(out, servedKind{group, plural, p})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no CRDs checked; testdata path is wrong")
	}
	return out
}

// readerRoleProblems returns what is wrong with role: a served kind it lacks get, list or watch on,
// or a grant beyond those verbs. The one subresource allowed is get on core pods/proxy.
func readerRoleProblems(role *rbacv1.ClusterRole, kinds []servedKind) []string {
	var out []string
	// granted[group/plural] holds the verbs the role grants on that resource.
	granted := map[string][]string{}
	for _, r := range role.Rules {
		if len(r.ResourceNames) > 0 || len(r.NonResourceURLs) > 0 {
			out = append(out, fmt.Sprintf("rule on %v uses resourceNames or nonResourceURLs", r.Resources))
		}
		for _, v := range r.Verbs {
			if !slices.Contains([]string{"get", "list", "watch"}, v) {
				out = append(out, fmt.Sprintf("rule on %v grants verb %q; only get, list and watch", r.Resources, v))
			}
		}
		for _, res := range r.Resources {
			if strings.Contains(res, "/") && (res != "pods/proxy" || !slices.Equal(r.APIGroups, []string{""}) || !slices.Equal(r.Verbs, []string{"get"})) {
				out = append(out, fmt.Sprintf("rule grants subresource %s in %v with %v; only get on core pods/proxy", res, r.APIGroups, r.Verbs))
			}
		}
		for _, g := range r.APIGroups {
			if g == "*" {
				out = append(out, "rule uses a * apiGroup; list groups explicitly")
			}
			for _, res := range r.Resources {
				if res == "*" {
					out = append(out, fmt.Sprintf("rule in group %q uses a * resource; list resources explicitly", g))
				}
				granted[g+"/"+res] = append(granted[g+"/"+res], r.Verbs...)
			}
		}
	}
	for _, k := range kinds {
		for _, verb := range []string{"get", "list", "watch"} {
			if !slices.Contains(granted[k.group+"/"+k.plural], verb) {
				out = append(out, fmt.Sprintf("kgwctl-reader lacks %s on %s/%s (from %s); update docs/rbac.yaml", verb, k.group, k.plural, k.src))
			}
		}
	}
	return out
}

// ruleFor returns the rule of role that lists resource.
func ruleFor(t *testing.T, role *rbacv1.ClusterRole, resource string) *rbacv1.PolicyRule {
	t.Helper()
	for i := range role.Rules {
		if slices.Contains(role.Rules[i].Resources, resource) {
			return &role.Rules[i]
		}
	}
	t.Fatalf("kgwctl-reader has no rule for %s", resource)
	return nil
}

// TestReaderRoleCoversServedKinds fails when the kgwctl-reader ClusterRole lacks get, list or watch on
// a served test CRD kind, or grants more than that.
func TestReaderRoleCoversServedKinds(t *testing.T) {
	role, kinds := readerRole(t), servedKinds(t)
	for _, p := range readerRoleProblems(role, kinds) {
		t.Error(p)
	}

	// Each mutated role must be rejected by the same check; gw is the gateways rule, px the pods/proxy one.
	for name, mutate := range map[string]func(gw, px *rbacv1.PolicyRule){
		"missing verb":             func(gw, _ *rbacv1.PolicyRule) { gw.Verbs = []string{"get", "list"} },
		"extra verb":               func(gw, _ *rbacv1.PolicyRule) { gw.Verbs = append(gw.Verbs, "create") },
		"wildcard verb":            func(gw, _ *rbacv1.PolicyRule) { gw.Verbs = []string{"*"} },
		"subresource":              func(gw, _ *rbacv1.PolicyRule) { gw.Resources = append(gw.Resources, "gateways/status") },
		"pods/log subresource":     func(_, px *rbacv1.PolicyRule) { px.Resources = append(px.Resources, "pods/log") },
		"pods/proxy extra verb":    func(_, px *rbacv1.PolicyRule) { px.Verbs = append(px.Verbs, "create") },
		"resourceNames":            func(gw, _ *rbacv1.PolicyRule) { gw.ResourceNames = []string{"some-gateway"} },
		"pods/proxy resourceNames": func(_, px *rbacv1.PolicyRule) { px.ResourceNames = []string{"some-pod"} },
		"nonResourceURLs":          func(gw, _ *rbacv1.PolicyRule) { gw.NonResourceURLs = []string{"/metrics"} },
	} {
		t.Run(name, func(t *testing.T) {
			r := role.DeepCopy()
			mutate(ruleFor(t, r, "gateways"), ruleFor(t, r, "pods/proxy"))
			if len(readerRoleProblems(r, kinds)) == 0 {
				t.Error("the mutated role passes; want a problem")
			}
		})
	}
}
