package proxyview_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
)

// modelTypes are the xdsview model types the payload is built from (DD-13).
// TestCoverageReachesAll checks that no model type a listed one holds is missing.
var modelTypes = []any{
	model.Ref{},
	model.Snapshot{}, model.Coverage{}, model.SectionCoverage{}, model.Warning{},
	model.Listener{}, model.ListenerState{}, model.ListenerError{},
	model.NetworkFilterChain{}, model.NetworkFilter{}, model.TLSContext{}, model.Discovery{},
	model.HCMConfig{}, model.HTTPFilter{}, model.RouteConfigUse{}, model.RouteConfigSource{},
	model.RouteConfig{}, model.VirtualHost{}, model.Route{}, model.RouteMatch{},
	model.HeaderMatch{}, model.QueryParamMatch{}, model.RouteAction{}, model.Redirect{},
	model.DirectResponse{}, model.Extension{}, model.RouteRewrite{}, model.WeightedCluster{},
	model.HeaderOperation{}, model.EffectiveChain{}, model.ChainEntry{},
}

// mapped names where each model field lands in the payload.
var mapped = map[string]string{
	"Ref.Group": "group", "Ref.Kind": "kind", "Ref.Namespace": "namespace", "Ref.Name": "name",
	"Snapshot.Capture": "capture", "Snapshot.Listeners": "listeners", "Snapshot.Coverage": "sections",
	"Snapshot.Warnings": "warnings", "Snapshot.RouteConfigs": "hcm.routeConfig (joined sources)",
	"Coverage.Sections": "sections", "SectionCoverage.Type": "sections[].type", "SectionCoverage.Status": "sections[].status",
	"Warning.Subject": "warnings[].subject", "Warning.Code": "warnings[].code", "Warning.Message": "warnings[].message",
	"Listener.ID": "id", "Listener.Name": "name", "Listener.Address": "address", "Listener.FilterChains": "filterChains",
	"Listener.State": "state", "Listener.Static": "static",
	"ListenerState.Status": "status", "ListenerState.Updating": "updating", "ListenerState.Draining": "draining",
	"ListenerState.LastUpdated": "lastUpdated", "ListenerState.Error": "error",
	"ListenerError.Details": "details", "ListenerError.LastAttempt": "lastAttempt",
	"NetworkFilterChain.ID": "id", "NetworkFilterChain.Name": "name", "NetworkFilterChain.TLS": "tls",
	"NetworkFilterChain.Default": "default", "NetworkFilterChain.NetworkFilters": "networkFilters", "NetworkFilterChain.HCM": "hcm",
	"NetworkFilter.ID": "id", "NetworkFilter.Name": "name", "NetworkFilter.Type": "type", "NetworkFilter.Discovery": "configSource",
	"TLSContext.SNIHosts": "sniHosts", "Discovery.Source": "configSource",
	"HCMConfig.ID": "id", "HCMConfig.RouteConfigName": "routeConfig.name", "HCMConfig.ScopedRoutes": "routeConfig.scopedRoutes",
	"HCMConfig.HTTPFilters": "httpFilters", "HCMConfig.UpstreamFilters": "upstreamFilters",
	"HCMConfig.RouteConfigUse": "routeConfig.source, routeConfig.state", "HCMConfig.RouteConfig": "virtualHosts",
	"HTTPFilter.ID": "slot", "HTTPFilter.Name": "name", "HTTPFilter.Type": "type", "HTTPFilter.Disabled": "disabled",
	"HTTPFilter.Discovery":  "configSource",
	"RouteConfigUse.Source": "routeConfig.source", "RouteConfigUse.State": "routeConfig.state",
	"RouteConfigSource.ID": "routeConfig.source", "RouteConfigSource.Kind": "routeConfig.kind", "RouteConfigSource.Name": "routeConfig.name",
	"RouteConfig.Name": "routeConfig.name", "RouteConfig.VirtualHosts": "virtualHosts",
	"VirtualHost.ID": "id", "VirtualHost.SourcePath": "sourcePath", "VirtualHost.Name": "name", "VirtualHost.Domains": "domains",
	"VirtualHost.Routes": "routes",
	"Route.ID":           "id", "Route.SourcePath": "sourcePath", "Route.Name": "name", "Route.Match": "match", "Route.Action": "action",
	"Route.Cluster": "backend.cluster", "Route.WeightedClusters": "weightedBackends", "Route.Rewrite": "rewrite",
	"Route.MirrorClusters": "mirrors", "Route.RequestHeadersToAdd": "requestHeadersToAdd",
	"Route.ResponseHeadersToAdd": "responseHeadersToAdd", "Route.ResponseHeadersToRemove": "responseHeadersToRemove",
	"RouteMatch.Prefix": "prefix", "RouteMatch.PathSeparatedPrefix": "pathSeparatedPrefix", "RouteMatch.Path": "path",
	"RouteMatch.Regex": "regex", "RouteMatch.Connect": "connect", "RouteMatch.CaseSensitive": "caseSensitive",
	"RouteMatch.Headers": "headers", "RouteMatch.QueryParams": "queryParams", "RouteMatch.Unparsed": "unparsed",
	"HeaderMatch.Name": "name", "HeaderMatch.Value": "value", "HeaderMatch.Kind": "kind", "HeaderMatch.IgnoreCase": "ignoreCase",
	"HeaderMatch.Invert": "invert", "HeaderMatch.MissingAsEmpty": "missingAsEmpty",
	"QueryParamMatch.Name": "name", "QueryParamMatch.Value": "value", "QueryParamMatch.Kind": "kind", "QueryParamMatch.IgnoreCase": "ignoreCase",
	"RouteAction.Kind": "kind", "RouteAction.ClusterHeader": "clusterHeader", "RouteAction.ClusterSpecifierPlugin": "clusterSpecifierPlugin",
	"RouteAction.InlineClusterSpecifierPlugin": "inlineClusterSpecifierPlugin", "RouteAction.Redirect": "redirect",
	"RouteAction.DirectResponse": "directResponse", "RouteAction.FilterActionType": "filterActionType",
	"Redirect.ResponseCode": "responseCode", "Redirect.Scheme": "scheme", "Redirect.Host": "host", "Redirect.Port": "port",
	"Redirect.Path": "path", "Redirect.PrefixRewrite": "prefixRewrite", "Redirect.RegexPattern": "regexPattern",
	"Redirect.RegexSubstitution": "regexSubstitution", "Redirect.PathRewrite": "pathRewrite", "Redirect.StripQuery": "stripQuery",
	"DirectResponse.Status": "status", "DirectResponse.BodySource": "bodySource", "DirectResponse.BodySize": "bodySize",
	"DirectResponse.BodyFormat": "bodyFormat",
	"Extension.Name":            "inlineClusterSpecifierPlugin.name", "Extension.Type": "inlineClusterSpecifierPlugin.type",
	"RouteRewrite.RegexPattern": "regexPattern", "RouteRewrite.Substitution": "substitution", "RouteRewrite.Prefix": "prefix",
	"WeightedCluster.ID": "id", "WeightedCluster.Name": "name", "WeightedCluster.Weight": "weight",
	"WeightedCluster.TypedPerFilterConfig": "overridesFilters",
	"HeaderOperation.Key":                  "key", "HeaderOperation.Value": "value",
	"EffectiveChain.Filters": "filters", "EffectiveChain.Conditional": "chainConditional",
	"ChainEntry.Slot": "slot", "ChainEntry.Name": "name", "ChainEntry.Type": "type", "ChainEntry.Runs": "runs",
	"ChainEntry.State": "state", "ChainEntry.Level": "level", "ChainEntry.PerFilterType": "perFilterType",
	"ChainEntry.Request": "request", "ChainEntry.Response": "response", "ChainEntry.Wrapper": "wrapper",
	"ChainEntry.Conditional": "conditional", "ChainEntry.ConfigSource": "configSource",
}

// leftOut are the model fields the payload leaves out on purpose, with why.
var leftOut = map[string]string{
	"Listener.Metadata":                      "raw metadata (DD-14); its policy records are in policies",
	"HTTPFilter.TypedConfig":                 "raw config (DD-14)",
	"RouteConfig.TypedPerFilterConfig":       "raw config (DD-14); its effect is in filters[].level",
	"VirtualHost.TypedPerFilterConfig":       "raw config (DD-14); its effect is in filters[].level",
	"VirtualHost.Metadata":                   "raw metadata (DD-14)",
	"Route.TypedPerFilterConfig":             "raw config (DD-14); its effect is in filters[].level",
	"Route.Metadata":                         "raw metadata (DD-14); readings and policies come from it",
	"RouteConfigSource.Metadata":             "raw metadata (DD-14)",
	"RouteConfigSource.TypedPerFilterConfig": "raw config (DD-14)",
	"RouteConfigSource.Reported":             "an inline config's static entry; no join semantics (DD-23)",
	"RouteConfigUse.ID":                      "the use's occurrence ID; routeConfig has none",
	"Discovery.TypeURLs":                     "discovery details; only configSource is shown",
	"Discovery.DefaultConfig":                "discovery details; only configSource is shown",
	"Discovery.ApplyDefaultWithoutWarming":   "discovery details; only configSource is shown",
	"HeaderMatch.Regex":                      "the same as kind regex",
	"QueryParamMatch.Regex":                  "the same as kind regex",
}

// TestPayloadCoversModel fails when an xdsview model field is neither mapped
// nor left out, so a bump cannot drop data in silence.
func TestPayloadCoversModel(t *testing.T) {
	problems, seen := coverage(modelTypes)
	for _, p := range problems {
		t.Error(p)
	}
	for _, table := range []map[string]string{mapped, leftOut} {
		for key := range table {
			if !seen[key] {
				t.Errorf("table names %s, which the model lacks", key)
			}
		}
	}
}

// coverage returns each exported field of types that is neither or both
// mapped and left out, and every field key it saw.
func coverage(types []any) (problems []string, seen map[string]bool) {
	seen = map[string]bool{}
	for _, v := range types {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			key := typ.Name() + "." + f.Name
			seen[key] = true
			_, m := mapped[key]
			_, l := leftOut[key]
			switch {
			case !m && !l:
				problems = append(problems, fmt.Sprintf("model field %s (%s) is neither mapped nor left out", key, f.Type))
			case m && l:
				problems = append(problems, fmt.Sprintf("model field %s is both mapped and left out", key))
			}
		}
	}
	return problems, seen
}

// TestCoverageListsRef: model.Ref, which the payload projects directly, is covered.
func TestCoverageListsRef(t *testing.T) {
	for _, v := range modelTypes {
		if reflect.TypeOf(v) == reflect.TypeOf(model.Ref{}) {
			return
		}
	}
	t.Error("modelTypes lacks model.Ref")
}

// TestCoverageFailsOnNewField: a copy of model.Ref with one more exported
// field fails the check until that field is mapped or left out.
func TestCoverageFailsOnNewField(t *testing.T) {
	type Ref struct {
		Group, Kind, Namespace, Name string
		SectionName                  string // the added field
	}
	problems, _ := coverage([]any{Ref{}})
	if len(problems) != 1 || !strings.Contains(problems[0], "Ref.SectionName") {
		t.Errorf("problems %q, want one for Ref.SectionName", problems)
	}
}

// TestCoverageReachesAll: every xdsview model struct a listed type holds is
// listed too, so a new nested type cannot bypass the check.
func TestCoverageReachesAll(t *testing.T) {
	listed := map[reflect.Type]bool{}
	for _, v := range modelTypes {
		listed[reflect.TypeOf(v)] = true
	}
	modelPkg := reflect.TypeOf(model.Ref{}).PkgPath()
	var visit func(reflect.Type)
	seen := map[reflect.Type]bool{}
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ.PkgPath() != modelPkg || seen[typ] {
			return
		}
		seen[typ] = true
		if !listed[typ] {
			t.Errorf("model type %s is reachable but not in modelTypes", typ.Name())
		}
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				visit(typ.Field(i).Type)
			}
		}
	}
	for typ := range listed {
		visit(typ)
	}
}
