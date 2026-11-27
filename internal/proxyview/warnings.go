package proxyview

import (
	"fmt"

	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
)

// Warning is one parser or provenance warning about the whole dump.
type Warning struct {
	Subject string `json:"subject,omitempty"` // an occurrence ID; empty for a dump-wide warning
	Code    string `json:"code"`
	Message string `json:"message"`
}

// warningList is the one warning list of DD-12: every parser warning of full,
// then every index warning with kgwctl's message, each (subject, code,
// message) once. Index messages carry claim IDs and module names (DD-10), so
// they are never copied.
func warningList(full *model.Snapshot, ix *provenance.Index) []Warning {
	var out []Warning
	seen := map[Warning]bool{}
	add := func(w Warning) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, w := range full.Warnings {
		add(Warning{Subject: string(w.Subject), Code: w.Code, Message: w.Message})
	}
	for _, w := range ix.Warnings() {
		subject := describeSubject(full, w.Subject)
		var msg string
		switch w.Code {
		case model.WarnClaimDropped:
			msg = fmt.Sprintf("a provenance claim about %s broke a composition rule and was dropped", subject)
		case model.WarnLinkDropped:
			msg = fmt.Sprintf("a policy-to-filter link on %s could not be resolved and was dropped", subject)
		default:
			msg = fmt.Sprintf("provenance warning %s about %s", w.Code, subject)
		}
		add(Warning{Subject: string(w.Subject), Code: w.Code, Message: msg})
	}
	return out
}

// describeSubject names the occurrence id of full in words, such as
// `route x in virtual host y`; `occurrence <id>` when full lacks it.
func describeSubject(full *model.Snapshot, id model.ID) string {
	if id == "" {
		return "the dump"
	}
	for _, s := range full.RouteConfigs {
		if s.ID == id {
			return "route config " + s.Name
		}
	}
	for _, l := range full.Listeners {
		if !id.Within(l.ID) {
			continue
		}
		in := " in listener " + l.Name
		if l.ID == id {
			return "listener " + l.Name
		}
		for _, fc := range l.FilterChains {
			if fc.ID == id {
				return "filter chain " + fc.Name + in
			}
			for _, nf := range fc.NetworkFilters {
				if nf.ID == id {
					return "network filter " + nf.Name + in
				}
			}
			if s, ok := describeHCM(fc.HCM, id, in); ok {
				return s
			}
		}
	}
	return "occurrence " + string(id)
}

// describeHCM describes id when it lies inside hcm.
func describeHCM(hcm *model.HCMConfig, id model.ID, in string) (string, bool) {
	if hcm == nil || !id.Within(hcm.ID) {
		return "", false
	}
	for _, f := range hcm.HTTPFilters {
		if f.ID == id {
			return "filter " + f.Name + in, true
		}
	}
	for _, f := range hcm.UpstreamFilters {
		if f.ID == id {
			return "upstream filter " + f.Name + in, true
		}
	}
	if hcm.RouteConfigUse.ID == id {
		return "route config " + hcm.RouteConfigName + in, true
	}
	if rc := hcm.RouteConfig; rc != nil {
		for _, vh := range rc.VirtualHosts {
			if vh.ID == id {
				return "virtual host " + vh.Name, true
			}
			for _, r := range vh.Routes {
				if r.ID == id {
					return "route " + r.Name + " in virtual host " + vh.Name, true
				}
				for _, w := range r.WeightedClusters {
					if w.ID == id {
						return "backend " + w.Name + " of route " + r.Name, true
					}
				}
			}
		}
	}
	return "HTTP connection manager" + in, true
}
