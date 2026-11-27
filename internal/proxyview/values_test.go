package proxyview_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/proxyview"
)

// TestPayloadMappedValues: route fields the goldens leave empty reach the
// payload with their values, so a dropped assignment fails here, not only in coverage.
func TestPayloadMappedValues(t *testing.T) {
	v := build(t, handBuilt(t, "mapped-values.json"), proxyview.Options{})
	var doc map[string]any
	if err := json.Unmarshal([]byte(marshal(t, v.Payload(proxyview.Source{}))), &doc); err != nil {
		t.Fatal(err)
	}
	routes := map[string]any{}
	for _, r := range get(t, doc, "listeners.0.filterChains.0.hcm.virtualHosts.0.routes").([]any) {
		routes[r.(map[string]any)["name"].(string)] = r
	}
	for route, want := range map[string]map[string]string{
		"rewrite": {
			"rewrite.regexPattern": "^/a/(.*)", "rewrite.substitution": `/b/\1`, "rewrite.prefix": "/p",
			"match.caseSensitive": "false", "match.unparsed.0": "grpc",
			"match.headers.0.value": "hv", "match.headers.0.kind": "exact", "match.headers.0.ignoreCase": "true",
			"match.headers.0.invert": "true", "match.headers.0.missingAsEmpty": "true",
			"match.queryParams.0.name": "q", "match.queryParams.0.value": "qv", "match.queryParams.0.ignoreCase": "true",
			"action.clusterHeader": "x-cluster", "mirrors.0.cluster": "mirror-c",
			"requestHeadersToAdd.0.key": "x-req", "requestHeadersToAdd.0.value": "req-v",
			"responseHeadersToAdd.0.key": "x-resp", "responseHeadersToAdd.0.value": "resp-v", "responseHeadersToRemove.0": "x-rm",
		},
		"split": {"weightedBackends.0.name": "wc-a", "weightedBackends.0.weight": "70", "weightedBackends.0.overridesFilters": "true"},
		"redirect-prefix": {
			"action.kind": "redirect", "action.redirect.responseCode": "302", "action.redirect.scheme": "https",
			"action.redirect.host": "h.example", "action.redirect.port": "8443", "action.redirect.prefixRewrite": "/new",
			"action.redirect.stripQuery": "true",
		},
		"redirect-regex":        {"action.redirect.path": "/abs", "action.redirect.regexPattern": "^/r2", "action.redirect.regexSubstitution": "/rx"},
		"redirect-path-rewrite": {"action.redirect.pathRewrite": "/templated"},
		"direct": {
			"action.directResponse.status": "503", "action.directResponse.bodySource": "inline",
			"action.directResponse.bodySize": "4", "action.directResponse.bodyFormat": "true",
		},
		"plugin":        {"action.clusterSpecifierPlugin": "csp-name"},
		"inline-plugin": {"action.inlineClusterSpecifierPlugin.name": "icsp-name", "action.inlineClusterSpecifierPlugin.type": "type.googleapis.com/google.protobuf.Struct"},
		"filter-action": {"action.filterActionType": "type.googleapis.com/google.protobuf.Struct"},
		"connect":       {"match.connect": "true"},
	} {
		r, ok := routes[route]
		if !ok {
			t.Errorf("route %s missing from the payload", route)
			continue
		}
		for path, w := range want {
			if got := fmt.Sprint(get(t, r, path)); got != w {
				t.Errorf("%s %s = %s, want %s", route, path, got, w)
			}
		}
	}
}

// get walks a decoded JSON value by a dotted path; numbers index arrays.
// A missing step gives nil.
func get(t *testing.T, v any, path string) any {
	t.Helper()
	for _, step := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[step]
		case []any:
			i, err := strconv.Atoi(step)
			if err != nil || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}
