package proxyview

import (
	"slices"
	"time"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
)

// configUnresolved is the configSource of a filter whose discovered config
// xdsview could not establish for this occurrence.
const configUnresolved = "unresolved"

// ProxyRoutes is the `item` of the ProxyRoutes envelope (design §5). It is
// kgwctl's own projection of the snapshot, chains and claim index; it holds
// no claim ID, no module name and no raw config (DD-10, DD-14).
type ProxyRoutes struct {
	Source    Source        `json:"source"`
	Capture   string        `json:"capture"`
	Listeners []Listener    `json:"listeners"`
	Sections  []Section     `json:"sections"`
	Selection *SelectionOut `json:"selection,omitempty"`
	Warnings  []Warning     `json:"warnings,omitempty"`
}

// Source says where a config dump came from: a live replica (Gateway, Pod and
// CapturedAt) or a file (File, and Gateway when --gateway named it).
type Source struct {
	Gateway    *Ref       `json:"gateway,omitempty"`
	Pod        *Ref       `json:"pod,omitempty"`
	CapturedAt *time.Time `json:"capturedAt,omitempty"`
	File       string     `json:"file,omitempty"`
}

// Ref names a Kubernetes object; an empty group is omitted.
type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Resolved is what the claims say about one kind of attribution: the distinct
// candidates at the highest certainty (DD-17).
type Resolved[T any] struct {
	Certainty  string `json:"certainty"`
	Candidates []T    `json:"candidates"`
}

// Listener is one Envoy listener.
type Listener struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Address      string                  `json:"address,omitempty"`
	Static       bool                    `json:"static,omitempty"`
	State        ListenerState           `json:"state"`
	Port         *Resolved[ListenerPort] `json:"port,omitempty"`
	Gateway      *Resolved[Ref]          `json:"gateway,omitempty"`
	Policies     []Policy                `json:"policies,omitempty"`
	FilterChains []FilterChain           `json:"filterChains"`
}

// ListenerState is the listener version shown and its last error.
type ListenerState struct {
	Status      string         `json:"status,omitempty"`
	Updating    bool           `json:"updating,omitempty"`
	Draining    bool           `json:"draining,omitempty"`
	LastUpdated string         `json:"lastUpdated,omitempty"`
	Error       *ListenerError `json:"error,omitempty"`
}

// ListenerError is the last update Envoy rejected.
type ListenerError struct {
	Details     string `json:"details"`
	LastAttempt string `json:"lastAttempt,omitempty"`
}

// ListenerPort is the Gateway listener a name reads as: a port or a section name.
type ListenerPort struct {
	Port    int    `json:"port,omitempty"`
	Section string `json:"section,omitempty"`
}

// FilterChain is one network filter chain of a listener.
type FilterChain struct {
	ID             string          `json:"id"`
	Name           string          `json:"name,omitempty"`
	Default        bool            `json:"default,omitempty"`
	TLS            *TLS            `json:"tls,omitempty"`
	NetworkFilters []NetworkFilter `json:"networkFilters,omitempty"`
	HCM            *HCM            `json:"hcm,omitempty"`
}

// TLS is a filter chain's TLS match.
type TLS struct {
	SNIHosts []string `json:"sniHosts"`
}

// NetworkFilter is one network filter of a chain.
type NetworkFilter struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type,omitempty"`
	ConfigSource string `json:"configSource,omitempty"`
}

// HCM is an HTTP connection manager.
type HCM struct {
	ID              string        `json:"id"`
	RouteConfig     RouteConfig   `json:"routeConfig"`
	HTTPFilters     []HTTPFilter  `json:"httpFilters"`
	UpstreamFilters []HTTPFilter  `json:"upstreamFilters,omitempty"`
	DefaultChain    []Filter      `json:"defaultChain,omitempty"`
	VirtualHosts    []VirtualHost `json:"virtualHosts"`
}

// RouteConfig is an HCM's use of a route configuration.
type RouteConfig struct {
	Name         string   `json:"name,omitempty"`
	Source       string   `json:"source,omitempty"`
	State        string   `json:"state"`
	Kind         string   `json:"kind,omitempty"`
	ScopedRoutes bool     `json:"scopedRoutes,omitempty"`
	Policies     []Policy `json:"policies,omitempty"`
}

// HTTPFilter is one HTTP filter slot of an HCM.
type HTTPFilter struct {
	Slot         string               `json:"slot"`
	Name         string               `json:"name"`
	Type         string               `json:"type,omitempty"`
	Disabled     bool                 `json:"disabled,omitempty"`
	ConfigSource string               `json:"configSource,omitempty"`
	Extension    *Resolved[Extension] `json:"extension,omitempty"`
}

// Extension says what a filter slot is and which object defines it.
type Extension struct {
	Ref        *Ref   `json:"ref,omitempty"`
	FilterKind string `json:"filterKind,omitempty"`
	Helper     bool   `json:"helper,omitempty"`
}

// VirtualHost is one virtual host of a route config use.
type VirtualHost struct {
	ID         string                  `json:"id"`
	SourcePath string                  `json:"sourcePath,omitempty"`
	Name       string                  `json:"name"`
	Domains    []string                `json:"domains"`
	Host       *Resolved[string]       `json:"host,omitempty"`
	Port       *Resolved[ListenerPort] `json:"port,omitempty"`
	Policies   []Policy                `json:"policies,omitempty"`
	Routes     []Route                 `json:"routes"`
}

// Route is one Envoy route with everything kgwctl knows about it.
type Route struct {
	ID                      string            `json:"id"`
	SourcePath              string            `json:"sourcePath,omitempty"`
	Name                    string            `json:"name"`
	Match                   Match             `json:"match"`
	Origin                  Origin            `json:"origin"`
	Filters                 []Filter          `json:"filters"`
	ChainConditional        bool              `json:"chainConditional,omitempty"`
	Policies                []Policy          `json:"policies,omitempty"`
	Action                  *Action           `json:"action,omitempty"`
	Backend                 *Backend          `json:"backend,omitempty"`
	WeightedBackends        []WeightedBackend `json:"weightedBackends,omitempty"`
	Mirrors                 []Mirror          `json:"mirrors,omitempty"`
	Rewrite                 *Rewrite          `json:"rewrite,omitempty"`
	RequestHeadersToAdd     []Header          `json:"requestHeadersToAdd,omitempty"`
	ResponseHeadersToAdd    []Header          `json:"responseHeadersToAdd,omitempty"`
	ResponseHeadersToRemove []string          `json:"responseHeadersToRemove,omitempty"`
}

// Match is what traffic a route matches.
type Match struct {
	Prefix              string            `json:"prefix,omitempty"`
	PathSeparatedPrefix string            `json:"pathSeparatedPrefix,omitempty"`
	Path                string            `json:"path,omitempty"`
	Regex               string            `json:"regex,omitempty"`
	Connect             bool              `json:"connect,omitempty"`
	CaseSensitive       *bool             `json:"caseSensitive,omitempty"`
	Headers             []HeaderMatch     `json:"headers,omitempty"`
	QueryParams         []QueryParamMatch `json:"queryParams,omitempty"`
	Unparsed            []string          `json:"unparsed,omitempty"`
}

// HeaderMatch is one header condition.
type HeaderMatch struct {
	Name           string `json:"name"`
	Value          string `json:"value,omitempty"`
	Kind           string `json:"kind"`
	IgnoreCase     bool   `json:"ignoreCase,omitempty"`
	Invert         bool   `json:"invert,omitempty"`
	MissingAsEmpty bool   `json:"missingAsEmpty,omitempty"`
}

// QueryParamMatch is one query parameter condition.
type QueryParamMatch struct {
	Name       string `json:"name"`
	Value      string `json:"value,omitempty"`
	Kind       string `json:"kind"`
	IgnoreCase bool   `json:"ignoreCase,omitempty"`
}

// Origin is where a route came from, read from its routeSource claims.
type Origin struct {
	Certainty string    `json:"certainty"`
	Complete  bool      `json:"complete,omitempty"`
	Narrowed  bool      `json:"narrowed,omitempty"`
	ChosenBy  string    `json:"chosenBy,omitempty"`
	Readings  []Reading `json:"readings"`
}

// Reading is one HTTPRoute, rule and match a route can come from.
type Reading struct {
	Route          Ref    `json:"route"`
	RuleIndex      int    `json:"ruleIndex"`
	RuleName       string `json:"ruleName,omitempty"`
	MatchIndex     int    `json:"matchIndex"`
	Certainty      string `json:"certainty"`
	IndexCertainty string `json:"indexCertainty,omitempty"`
	Incomplete     bool   `json:"incomplete,omitempty"`
	NotInCluster   bool   `json:"notInCluster,omitempty"`
}

// Filter is one HCM filter slot as a route runs it (chain.Effective).
type Filter struct {
	Slot          string               `json:"slot"`
	Name          string               `json:"name"`
	Type          string               `json:"type,omitempty"`
	Runs          string               `json:"runs"`
	State         string               `json:"state"`
	Level         string               `json:"level,omitempty"`
	PerFilterType string               `json:"perFilterType,omitempty"`
	Request       string               `json:"request"`
	Response      string               `json:"response"`
	Wrapper       string               `json:"wrapper,omitempty"`
	Conditional   bool                 `json:"conditional,omitempty"`
	ConfigSource  string               `json:"configSource,omitempty"`
	Extension     *Resolved[Extension] `json:"extension,omitempty"`
	Policies      []Policy             `json:"policies,omitempty"`
}

// Policy is a policy record at one level (DD-16).
type Policy struct {
	Policy    Ref    `json:"policy"`
	Field     string `json:"field,omitempty"`
	Level     string `json:"level"`
	Certainty string `json:"certainty"`
}

// Action is what a route does with a request it matches.
type Action struct {
	Kind                         string          `json:"kind"`
	ClusterHeader                string          `json:"clusterHeader,omitempty"`
	ClusterSpecifierPlugin       string          `json:"clusterSpecifierPlugin,omitempty"`
	InlineClusterSpecifierPlugin *NamedType      `json:"inlineClusterSpecifierPlugin,omitempty"`
	Redirect                     *Redirect       `json:"redirect,omitempty"`
	DirectResponse               *DirectResponse `json:"directResponse,omitempty"`
	FilterActionType             string          `json:"filterActionType,omitempty"`
}

// NamedType is an extension's name and @type.
type NamedType struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// Redirect is a redirect action.
type Redirect struct {
	ResponseCode      int    `json:"responseCode"`
	Scheme            string `json:"scheme,omitempty"`
	Host              string `json:"host,omitempty"`
	Port              int    `json:"port,omitempty"`
	Path              string `json:"path,omitempty"`
	PrefixRewrite     string `json:"prefixRewrite,omitempty"`
	RegexPattern      string `json:"regexPattern,omitempty"`
	RegexSubstitution string `json:"regexSubstitution,omitempty"`
	PathRewrite       string `json:"pathRewrite,omitempty"`
	StripQuery        bool   `json:"stripQuery,omitempty"`
}

// DirectResponse is a direct response; the body itself is never shown.
type DirectResponse struct {
	Status     int    `json:"status"`
	BodySource string `json:"bodySource,omitempty"`
	BodySize   *int   `json:"bodySize,omitempty"`
	BodyFormat bool   `json:"bodyFormat,omitempty"`
}

// Rewrite is a route's path rewrite.
type Rewrite struct {
	RegexPattern string `json:"regexPattern,omitempty"`
	Substitution string `json:"substitution,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
}

// Header is a header a route adds.
type Header struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Backend is a route's cluster and the backend objects behind it.
type Backend struct {
	Cluster    string       `json:"cluster"`
	Certainty  string       `json:"certainty"`
	Candidates []BackendRef `json:"candidates"`
}

// BackendRef is one backend object and port.
type BackendRef struct {
	Backend Ref `json:"backend"`
	Port    int `json:"port,omitempty"`
}

// WeightedBackend is one backend of a traffic split.
type WeightedBackend struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Weight           int                  `json:"weight"`
	OverridesFilters bool                 `json:"overridesFilters,omitempty"`
	Backend          Resolved[BackendRef] `json:"backend"`
}

// Mirror is one mirror cluster of a route.
type Mirror struct {
	Cluster string               `json:"cluster"`
	Backend Resolved[BackendRef] `json:"backend"`
}

// SelectionOut is the --route selection over the whole dump (§5.6).
type SelectionOut struct {
	Query           Query            `json:"query"`
	Strategy        string           `json:"strategy"`
	KnownHTTPRoutes string           `json:"knownHTTPRoutes"` // listed, unavailable or notRead (R24)
	Routes          []MatchedRoute   `json:"routes"`
	Inferred        []InferredRoute  `json:"inferred"`
	Ambiguous       []AmbiguousRoute `json:"ambiguous"`
	Unresolved      []RouteKey       `json:"unresolved"`
}

// InferredRoute is a route --select exact listed, not matched: its one
// reading, or with --rule its indices, are below exact or unknown.
type InferredRoute struct {
	RouteKey
	Readings []Reading `json:"readings"`
}

// Query is --route and --rule.
type Query struct {
	Route     Ref  `json:"route"`
	RuleIndex *int `json:"ruleIndex,omitempty"`
}

// RouteKey identifies one route occurrence and names it.
type RouteKey struct {
	ID          string `json:"id"`
	RouteConfig string `json:"routeConfig"`
	VirtualHost string `json:"virtualHost"`
	Name        string `json:"name"`
}

// MatchedRoute is a route in listeners and why it was selected.
type MatchedRoute struct {
	RouteKey
	ChosenBy string `json:"chosenBy"`
}

// AmbiguousRoute is a route listed, not matched.
type AmbiguousRoute struct {
	RouteKey
	Readings []Reading `json:"readings"`
	Narrowed bool      `json:"narrowed,omitempty"`
}

// Section is one config dump section and what the parser did with it.
type Section struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// Payload returns v as the ProxyRoutes item, with src as its source.
func (v View) Payload(src Source) ProxyRoutes {
	p := ProxyRoutes{Source: src, Capture: string(v.Full.Capture), Listeners: []Listener{}, Sections: []Section{}, Warnings: v.Warnings}
	for _, s := range v.Full.Coverage.Sections {
		p.Sections = append(p.Sections, Section{Type: s.Type, Status: string(s.Status)})
	}
	for _, l := range v.Snapshot.Listeners {
		p.Listeners = append(p.Listeners, v.listener(l))
	}
	if v.Selected != nil {
		p.Selection = v.selectionOut()
	}
	return p
}

func (v View) listener(l model.Listener) Listener {
	out := Listener{ID: string(l.ID), Name: l.Name, Address: l.Address, Static: l.Static, FilterChains: []FilterChain{},
		State: ListenerState{Status: string(l.State.Status), Updating: l.State.Updating, Draining: l.State.Draining, LastUpdated: l.State.LastUpdated},
		Port:  listenerPort(v.Index, l.ID), Policies: policies(v.Index, l.ID)}
	if e := l.State.Error; e != nil {
		out.State.Error = &ListenerError{Details: e.Details, LastAttempt: e.LastAttempt}
	}
	if g := provenance.Resolve[provenance.GatewaySource](v.Index, l.ID); len(g.Claims) > 0 {
		out.Gateway = &Resolved[Ref]{Certainty: g.Certainty.String(), Candidates: []Ref{}}
		for _, c := range g.Candidates {
			out.Gateway.Candidates = append(out.Gateway.Candidates, ref(c.Gateway))
		}
	}
	for _, fc := range l.FilterChains {
		out.FilterChains = append(out.FilterChains, v.filterChain(fc))
	}
	return out
}

func (v View) filterChain(fc model.NetworkFilterChain) FilterChain {
	out := FilterChain{ID: string(fc.ID), Name: fc.Name, Default: fc.Default}
	if fc.TLS != nil {
		out.TLS = &TLS{SNIHosts: append([]string{}, fc.TLS.SNIHosts...)}
	}
	for _, nf := range fc.NetworkFilters {
		out.NetworkFilters = append(out.NetworkFilters, NetworkFilter{ID: string(nf.ID), Name: nf.Name, Type: nf.Type, ConfigSource: configSource(nf.Discovery)})
	}
	if fc.HCM != nil {
		h := v.hcm(*fc.HCM)
		out.HCM = &h
	}
	return out
}

func (v View) hcm(h model.HCMConfig) HCM {
	use := h.RouteConfigUse
	out := HCM{ID: string(h.ID), HTTPFilters: v.httpFilters(h.HTTPFilters), VirtualHosts: []VirtualHost{},
		RouteConfig: RouteConfig{Name: h.RouteConfigName, Source: string(use.Source), State: string(use.State), ScopedRoutes: h.ScopedRoutes}}
	if len(h.UpstreamFilters) > 0 {
		out.UpstreamFilters = v.httpFilters(h.UpstreamFilters)
	}
	if s, ok := v.Full.RouteConfigSource(use.Source); ok {
		out.RouteConfig.Name, out.RouteConfig.Kind = s.Name, string(s.Kind)
		out.RouteConfig.Policies = policies(v.Index, s.ID)
	}
	if h.RouteConfig == nil {
		out.DefaultChain = v.filters(use.ID, v.Chains[use.ID])
		return out
	}
	for _, vh := range h.RouteConfig.VirtualHosts {
		out.VirtualHosts = append(out.VirtualHosts, v.virtualHost(vh))
	}
	return out
}

func (v View) httpFilters(list []model.HTTPFilter) []HTTPFilter {
	out := []HTTPFilter{}
	for _, f := range list {
		out = append(out, HTTPFilter{Slot: string(f.ID), Name: f.Name, Type: f.Type, Disabled: f.Disabled,
			ConfigSource: configSource(f.Discovery), Extension: extension(v.Index, f.ID)})
	}
	return out
}

func (v View) virtualHost(vh model.VirtualHost) VirtualHost {
	out := VirtualHost{ID: string(vh.ID), SourcePath: string(vh.SourcePath), Name: vh.Name, Domains: append([]string{}, vh.Domains...),
		Port: listenerPort(v.Index, vh.ID), Policies: policies(v.Index, vh.ID), Routes: []Route{}}
	if h := provenance.Resolve[provenance.HostSource](v.Index, vh.ID); len(h.Claims) > 0 {
		out.Host = &Resolved[string]{Certainty: h.Certainty.String(), Candidates: []string{}}
		for _, c := range h.Candidates {
			out.Host.Candidates = append(out.Host.Candidates, c.Hostname)
		}
	}
	for _, r := range vh.Routes {
		out.Routes = append(out.Routes, v.route(r))
	}
	return out
}

func (v View) route(r model.Route) Route {
	c := v.Chains[r.ID]
	out := Route{ID: string(r.ID), SourcePath: string(r.SourcePath), Name: r.Name, Match: match(r.Match), Origin: v.origin(r.ID),
		Filters: v.filters(r.ID, c), ChainConditional: c.Conditional, Policies: policies(v.Index, r.ID),
		Action: action(r.Action), ResponseHeadersToRemove: r.ResponseHeadersToRemove,
		RequestHeadersToAdd: headers(r.RequestHeadersToAdd), ResponseHeadersToAdd: headers(r.ResponseHeadersToAdd)}
	if r.Cluster != "" {
		b := backend(provenance.Resolve[provenance.BackendSource](v.Index, r.ID))
		out.Backend = &Backend{Cluster: r.Cluster, Certainty: b.Certainty, Candidates: b.Candidates}
	}
	for _, w := range r.WeightedClusters {
		out.WeightedBackends = append(out.WeightedBackends, WeightedBackend{ID: string(w.ID), Name: w.Name, Weight: w.Weight,
			OverridesFilters: len(w.TypedPerFilterConfig) > 0, Backend: backend(provenance.Resolve[provenance.BackendSource](v.Index, w.ID))})
	}
	for _, m := range r.MirrorClusters {
		out.Mirrors = append(out.Mirrors, Mirror{Cluster: m, Backend: backend(provenance.ResolveMirror(v.Index, r.ID, m))})
	}
	if rw := r.Rewrite; rw != nil {
		out.Rewrite = &Rewrite{RegexPattern: rw.RegexPattern, Substitution: rw.Substitution, Prefix: rw.Prefix}
	}
	return out
}

// origin reads a route's readings; a narrowed route marks the readings Known set aside.
func (v View) origin(route model.ID) Origin {
	res := provenance.Resolve[provenance.RouteSource](v.Index, route)
	out := Origin{Certainty: res.Certainty.String(), Complete: complete(res), ChosenBy: chosenBy(v.chosen(route)), Readings: []Reading{}}
	n, narrowed := v.narrowing(route)
	out.Narrowed = narrowed
	kept := readings(n.Kept)
	for _, r := range effective(res) {
		rd := reading(r, topCertainty(res))
		rd.NotInCluster = narrowed && !containsReading(kept, r)
		out.Readings = append(out.Readings, rd)
	}
	return out
}

func chosenBy(chosen bool) string {
	if chosen {
		return "known"
	}
	return ""
}

// topCertainty is the highest certainty of a resolution's claims; Resolve
// rates disagreeing candidates ambiguous, but each reading keeps its own.
func topCertainty[T provenance.Assertion](res provenance.Resolution[T]) model.Certainty {
	top := model.Unknown
	for _, c := range res.Claims {
		top = max(top, c.Certainty)
	}
	return top
}

// complete reports whether the readings name every possible source: each at
// the highest certainty is exhaustive, and none at any certainty is incomplete.
func complete(res provenance.Resolution[provenance.RouteSource]) bool {
	top := topCertainty(res)
	if top < model.Ambiguous {
		return false
	}
	for _, c := range res.Claims {
		if a := c.Assertion.(provenance.RouteSource); a.Incomplete || (c.Certainty == top && !a.Exhaustive) {
			return false
		}
	}
	return true
}

func reading(r provenance.RouteSource, c model.Certainty) Reading {
	out := Reading{Route: ref(r.Route), RuleIndex: r.RuleIndex, RuleName: r.RuleName, MatchIndex: r.MatchIndex,
		Certainty: c.String(), Incomplete: r.Incomplete}
	if r.IndexCertainty != model.Unknown && r.IndexCertainty != c {
		out.IndexCertainty = r.IndexCertainty.String()
	}
	return out
}

// filters copies a chain from chain.Effective and adds the attribution of
// each slot on route: its extension and the policy records linked to it.
func (v View) filters(route model.ID, c model.EffectiveChain) []Filter {
	out := []Filter{}
	links := map[model.ID][]Policy{}
	for _, cl := range v.Index.About(route) {
		l, ok := cl.Assertion.(provenance.FilterLink)
		if !ok {
			continue
		}
		if rec, ok := v.Index.Record(cl); ok {
			p := rec.Assertion.(provenance.PolicyRecord)
			links[l.Slot] = append(links[l.Slot], policy(p, min(rec.Certainty, cl.Certainty)))
		}
	}
	for _, e := range c.Filters {
		cs := e.ConfigSource
		switch cs {
		case model.ConfigInline:
			cs = ""
		case "":
			cs = configUnresolved
		}
		out = append(out, Filter{Slot: string(e.Slot), Name: e.Name, Type: e.Type, Runs: string(e.Runs), State: string(e.State),
			Level: string(e.Level), PerFilterType: e.PerFilterType, Request: string(e.Request), Response: string(e.Response),
			Wrapper: e.Wrapper, Conditional: e.Conditional, ConfigSource: cs, Extension: extension(v.Index, e.Slot), Policies: links[e.Slot]})
	}
	return out
}

func (v View) selectionOut() *SelectionOut {
	s := v.Selected
	out := &SelectionOut{Query: Query{Route: ref(HTTPRouteRef(v.Selection.Route)), RuleIndex: v.Selection.Rule},
		Strategy: string(v.Strategy), KnownHTTPRoutes: string(v.KnownInput), Routes: []MatchedRoute{}, Inferred: []InferredRoute{}, Ambiguous: []AmbiguousRoute{}, Unresolved: []RouteKey{}}
	keys := routeKeys(v.Full)
	for _, id := range s.Routes {
		if slices.Contains(v.Inferred, id) {
			out.Inferred = append(out.Inferred, InferredRoute{RouteKey: keys[id], Readings: v.origin(id).Readings})
			continue
		}
		out.Routes = append(out.Routes, MatchedRoute{RouteKey: keys[id], ChosenBy: "select"})
	}
	for _, n := range v.Chosen {
		out.Routes = append(out.Routes, MatchedRoute{RouteKey: keys[n.Route], ChosenBy: "known"})
	}
	for _, id := range s.Ambiguous {
		if v.chosen(id) {
			continue
		}
		o := v.origin(id)
		out.Ambiguous = append(out.Ambiguous, AmbiguousRoute{RouteKey: keys[id], Readings: o.Readings, Narrowed: o.Narrowed})
	}
	for _, id := range s.Unresolved {
		out.Unresolved = append(out.Unresolved, keys[id])
	}
	return out
}

// routeKeys maps every route of snap to its key.
func routeKeys(snap *model.Snapshot) map[model.ID]RouteKey {
	out := map[model.ID]RouteKey{}
	walk(snap, func(h model.HCMConfig, vh model.VirtualHost, r model.Route) {
		out[r.ID] = RouteKey{ID: string(r.ID), RouteConfig: h.RouteConfig.Name, VirtualHost: vh.Name, Name: r.Name}
	})
	return out
}

// configSource maps a discovery to the payload indicator: absent inline,
// the resolved source, or unresolved.
func configSource(d *model.Discovery) string {
	switch {
	case d == nil:
		return ""
	case d.Source == "":
		return configUnresolved
	}
	return d.Source
}

func listenerPort(ix *provenance.Index, id model.ID) *Resolved[ListenerPort] {
	res := provenance.Resolve[provenance.ListenerSource](ix, id)
	if len(res.Claims) == 0 {
		return nil
	}
	out := &Resolved[ListenerPort]{Certainty: res.Certainty.String(), Candidates: []ListenerPort{}}
	for _, c := range res.Candidates {
		out.Candidates = append(out.Candidates, ListenerPort{Port: c.Port, Section: c.Section})
	}
	return out
}

func extension(ix *provenance.Index, slot model.ID) *Resolved[Extension] {
	res := provenance.Resolve[provenance.ExtensionSource](ix, slot)
	if len(res.Claims) == 0 {
		return nil
	}
	out := &Resolved[Extension]{Certainty: res.Certainty.String(), Candidates: []Extension{}}
	for _, c := range res.Candidates {
		e := Extension{FilterKind: c.FilterKind, Helper: c.Helper}
		if c.Extension != nil {
			r := ref(*c.Extension)
			e.Ref = &r
		}
		out.Candidates = append(out.Candidates, e)
	}
	return out
}

// policies returns the policy records on subject, in index order.
func policies(ix *provenance.Index, subject model.ID) []Policy {
	var out []Policy
	for _, c := range ix.About(subject) {
		if p, ok := c.Assertion.(provenance.PolicyRecord); ok {
			out = append(out, policy(p, c.Certainty))
		}
	}
	return out
}

func policy(p provenance.PolicyRecord, c model.Certainty) Policy {
	return Policy{Policy: ref(p.Policy), Field: p.Field, Level: string(p.Level), Certainty: c.String()}
}

func backend(res provenance.Resolution[provenance.BackendSource]) Resolved[BackendRef] {
	out := Resolved[BackendRef]{Certainty: res.Certainty.String(), Candidates: []BackendRef{}}
	for _, c := range res.Candidates {
		out.Candidates = append(out.Candidates, BackendRef{Backend: ref(c.Backend), Port: c.Port})
	}
	return out
}

func ref(r model.Ref) Ref {
	return Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

func match(m model.RouteMatch) Match {
	out := Match{Prefix: m.Prefix, PathSeparatedPrefix: m.PathSeparatedPrefix, Path: m.Path, Regex: m.Regex,
		Connect: m.Connect, CaseSensitive: m.CaseSensitive, Unparsed: m.Unparsed}
	for _, h := range m.Headers {
		out.Headers = append(out.Headers, HeaderMatch{Name: h.Name, Value: h.Value, Kind: string(h.Kind), IgnoreCase: h.IgnoreCase, Invert: h.Invert, MissingAsEmpty: h.MissingAsEmpty})
	}
	for _, q := range m.QueryParams {
		out.QueryParams = append(out.QueryParams, QueryParamMatch{Name: q.Name, Value: q.Value, Kind: string(q.Kind), IgnoreCase: q.IgnoreCase})
	}
	return out
}

func action(a model.RouteAction) *Action {
	if a.Kind == "" {
		return nil
	}
	out := &Action{Kind: string(a.Kind), ClusterHeader: a.ClusterHeader, ClusterSpecifierPlugin: a.ClusterSpecifierPlugin, FilterActionType: a.FilterActionType}
	if x := a.InlineClusterSpecifierPlugin; x != nil {
		out.InlineClusterSpecifierPlugin = &NamedType{Name: x.Name, Type: x.Type}
	}
	if r := a.Redirect; r != nil {
		out.Redirect = &Redirect{ResponseCode: r.ResponseCode, Scheme: r.Scheme, Host: r.Host, Port: r.Port, Path: r.Path, PrefixRewrite: r.PrefixRewrite,
			RegexPattern: r.RegexPattern, RegexSubstitution: r.RegexSubstitution, PathRewrite: r.PathRewrite, StripQuery: r.StripQuery}
	}
	if d := a.DirectResponse; d != nil {
		out.DirectResponse = &DirectResponse{Status: d.Status, BodySource: d.BodySource, BodySize: d.BodySize, BodyFormat: d.BodyFormat}
	}
	return out
}

func headers(list []model.HeaderOperation) []Header {
	var out []Header
	for _, h := range list {
		out = append(out, Header{Key: h.Key, Value: h.Value})
	}
	return out
}
