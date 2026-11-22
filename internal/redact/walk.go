package redact

import (
	"bytes"
	"regexp"
	"strings"
)

// mode selects the structured rule set: json is the base, config-dump and
// object add their own rules on top (design §7 table).
type mode int

const (
	modeJSON mode = iota
	modeConfigDump
	modeObject
)

// zone carries what a node's ancestors make of it.
type zone uint16

const (
	// zHeaderValue: value and raw_value strings below are header values.
	zHeaderValue zone = 1 << iota
	// zHeaderMap: every direct string child is a header value.
	zHeaderMap
	// zPair: strings below are the value of a sensitive pair.
	zPair
	// zLua: inline sources below are Lua code.
	zLua
	// zJWKS: inline sources below are a JWKS.
	zJWKS
	// zBasicAuth: users below are htpasswd lines.
	zBasicAuth
	// zMapKeys: direct children's keys are map keys.
	zMapKeys
	// zMapAll: every key below is a map key.
	zMapAll
	// zKeyName: a key above names a credential; scalars below are redacted.
	zKeyName
)

// refKey is a member that names or locates a credential rather than holding
// it, such as secret.secretName; it stays under zKeyName.
func refKey(key string) bool {
	switch key {
	case "name", "namespace", "kind", "group", "apiVersion", "path", "type", "@type":
		return true
	}
	return strings.HasSuffix(key, "Name") || strings.HasSuffix(key, "_name") || strings.HasSuffix(key, "Namespace")
}

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// pairValueKeys are the members of a pair that hold its value: Kubernetes and
// Envoy header values, and Envoy header matchers.
var pairValueKeys = map[string]bool{
	"value": true, "raw_value": true, "string_match": true, "exact_match": true,
	"prefix_match": true, "suffix_match": true, "contains_match": true, "safe_regex_match": true,
}

// objectMapFields are Kubernetes map fields whose keys are user data.
var objectMapFields = map[string]bool{
	"labels": true, "annotations": true, "matchLabels": true, "nodeSelector": true, "extraLabels": true,
	"extraAnnotations": true, "data": true, "stringData": true, "binaryData": true,
}

// fieldRe is a key shown literally in a location; anything else is [key].
var fieldRe = regexp.MustCompile(`^[a-z@][A-Za-z0-9_]{0,63}$`)

type walker struct {
	mode mode
	kind string
	opts Options
	rec  *recorder
}

// walk redacts n in place; key is the member key that holds n.
func (w *walker) walk(n *node, at, key string, z zone) {
	switch n.kind {
	case kindObject:
		w.object(n, at, key, z)
	case kindArray:
		for _, e := range n.elems {
			if e.kind == kindString {
				w.str(e, at+"[]", key, z, false, false)
				continue
			}
			w.walk(e, at+"[]", key, z&^(zMapKeys|zHeaderMap))
		}
	case kindString:
		w.str(n, at, key, z, false, false)
	}
}

func (w *walker) object(n *node, at, key string, z zone) {
	name, _ := n.get("name")
	if name == "" {
		name, _ = n.get("key")
	}
	pair := name != "" && w.opts.sensitiveHeader(name)
	env := w.mode == modeObject && key == "env" && name != "" && !keptEnvName(name)
	typ, _ := n.get("@type")
	wrapper := w.mode == modeConfigDump && typ == "type.googleapis.com/google.protobuf.StringValue"

	kept := n.members[:0]
	for _, m := range n.members {
		loc := w.loc(at, m.key, z)
		if w.dropped(at, m.key, loc) {
			continue
		}
		kept = append(kept, m)
		cz := w.childZone(z, key, m.key)
		pairValue := pair && pairValueKeys[m.key]
		switch {
		case wrapper && m.key == "value" && m.val.kind == kindString:
			w.stringValue(m.val, loc)
		case m.val.kind == kindString:
			w.str(m.val, loc, m.key, cz|z&zHeaderMap, pairValue, env && m.key == "value")
		case pairValue:
			w.walk(m.val, loc, m.key, cz|zPair)
		default:
			w.walk(m.val, loc, m.key, cz)
		}
	}
	n.members = kept
}

// loc is the location of member key of the object at at.
func (w *walker) loc(at, key string, z zone) string {
	if z&(zMapKeys|zMapAll) != 0 || !fieldRe.MatchString(key) {
		return at + "[key]"
	}
	if _, hits := patternPass(key); hits > 0 {
		return at + "[key]"
	}
	return joinLoc(at, key)
}

// dropped applies the managed-fields and last-applied rules.
func (w *walker) dropped(at, key, loc string) bool {
	if w.mode != modeObject {
		return false
	}
	switch {
	case key == "managedFields" && (at == "metadata" || strings.HasSuffix(at, ".metadata")):
		w.rec.add(RuleManagedFields, loc, 1)
		return true
	case key == lastApplied && strings.HasSuffix(at, "metadata.annotations"):
		w.rec.add(RuleLastApplied, loc, 1)
		return true
	}
	return false
}

// childZone is the zone of the children of member key, whose object is held by parent.
func (w *walker) childZone(z zone, parent, key string) zone {
	z &^= zMapKeys | zHeaderMap
	if sensitiveKey(key) {
		z |= zKeyName
	}
	switch w.mode {
	case modeConfigDump:
		switch key {
		case "request_headers_to_add", "response_headers_to_add", "headers_to_add",
			"mutations", "request_mutations", "response_mutations", "initial_metadata":
			z |= zHeaderValue
		case "source_codes":
			z |= zLua | zMapKeys
		case "default_source_code":
			z |= zLua
		case "local_jwks":
			z |= zJWKS
		case "filter_metadata":
			z |= zMapAll
		case "typed_filter_metadata", "typed_per_filter_config", "providers",
			"typed_extension_protocol_options", "requirement_map":
			z |= zMapKeys
		}
	case modeObject:
		switch {
		// request, response and early header modifiers of routes and ListenerPolicy
		case strings.HasSuffix(key, "HeaderModifier"), key == "headerModifiers", key == "initialMetadata":
			z |= zHeaderValue
		case key == "headers":
			if parent == "tunnel" || strings.HasSuffix(w.kind, "ListenerPolicy") {
				z |= zHeaderValue
			}
		case key == "headersToAdd":
			z |= zHeaderMap | zMapKeys
		case key == "basicAuth":
			z |= zBasicAuth
		case objectMapFields[key]:
			z |= zMapKeys
		}
	}
	return z
}

// str applies the first string rule that matches, in design §7 order.
func (w *walker) str(n *node, at, key string, z zone, pairValue, envValue bool) {
	if n.str == Marker || n.str == envoyMarkerBytes {
		return
	}
	cd := w.mode == modeConfigDump
	inline := key == "inline_string" || key == "inline_bytes"
	switch {
	case cd && key == "inline_code", z&zLua != 0 && inline:
		w.set(n, RuleLua, at)
	case z&zJWKS != 0 && inline:
		w.jwks(n, at, key == "inline_bytes")
	case cd && inline:
		w.set(n, RuleInline, at)
	case z&zHeaderMap != 0, z&zHeaderValue != 0 && (key == "value" || key == "raw_value"):
		w.set(n, RuleHeaderValue, at)
	case envValue:
		w.set(n, RuleField, at)
	case pairValue, z&zPair != 0:
		w.set(n, RuleHeaderPair, at)
	case z&zBasicAuth != 0 && key == "users":
		w.basicAuth(n, at)
	case w.mode == modeObject && w.kind == "DirectResponse" && at == "spec.body":
		w.set(n, RuleField, at)
	case w.mode == modeObject && key == "inline" && strings.HasSuffix(at, "jwks.local.inline"):
		w.jwks(n, at, false)
	case sensitiveKey(key), z&zKeyName != 0 && !refKey(key):
		w.set(n, RuleKeyName, at)
	default:
		s, hits := patternPass(n.str)
		n.str = s
		w.rec.add(RulePattern, at, hits)
	}
}

func (w *walker) set(n *node, rule Rule, at string) {
	n.str = Marker
	w.rec.add(rule, at, 1)
}

// basicAuth keeps the user of an htpasswd line and redacts the hash.
func (w *walker) basicAuth(n *node, at string) {
	user, _, ok := strings.Cut(n.str, ":")
	n.str = Marker
	if ok {
		n.str = user + ":" + Marker
	}
	w.rec.add(RuleField, at, 1)
}

// stringValue applies the json rules inside a JSON StringValue, such as the
// dynamic-module transformation config; other text gets the pattern pass.
func (w *walker) stringValue(n *node, at string) {
	t, err := parseTree([]byte(n.str))
	if err != nil || (t.kind != kindObject && t.kind != kindArray) {
		w.str(n, at, "value", 0, false, false)
		return
	}
	sub := &walker{mode: modeJSON, opts: w.opts, rec: w.rec}
	sub.walk(t, at, "", 0)
	var b bytes.Buffer
	t.encode(&b)
	n.str = b.String()
}
