package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

// jwkCommon are the members every projected key may keep.
var jwkCommon = map[string]bool{
	"kty": true, "kid": true, "alg": true, "use": true, "key_ops": true,
	"x5c": true, "x5t": true, "x5t#S256": true,
}

// jwkPublic are the public members per key type; other types are redacted whole.
var jwkPublic = map[string]map[string]bool{
	"RSA": {"n": true, "e": true},
	"EC":  {"crv": true, "x": true, "y": true},
	"OKP": {"crv": true, "x": true},
}

// jwkPrivate are private or symmetric members, named in a location; other
// dropped members are shown as [key].
var jwkPrivate = map[string]bool{
	"d": true, "p": true, "q": true, "dp": true, "dq": true, "qi": true, "oth": true, "k": true,
}

// JWKS reduces a JSON Web Key Set to its public projection (design §7): RSA
// n and e, EC crv, x and y, OKP crv and x, plus kid, alg, use, key_ops, x5c,
// x5t and x5t#S256, rebuilt as {"keys":[...]}. Values must be strings or
// string arrays. Any other shape, type or kty gives Marker. Finding locations
// are relative to the JWKS value; "" is the whole value.
func JWKS(s string) (string, []Finding) {
	rec := &recorder{}
	out, ok := projectJWKS(s, rec)
	if !ok {
		return Marker, []Finding{{Rule: RuleJWKS, Count: 1}}
	}
	return out, rec.findings()
}

func projectJWKS(s string, rec *recorder) (string, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &top); err != nil || top == nil {
		return "", false
	}
	var keys []map[string]json.RawMessage
	if err := json.Unmarshal(top["keys"], &keys); err != nil || keys == nil {
		return "", false
	}
	for k := range top {
		if k != "keys" {
			rec.add(RuleJWKS, "[key]", 1)
		}
	}
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		var kty string
		if key == nil || json.Unmarshal(key["kty"], &kty) != nil || jwkPublic[kty] == nil {
			return "", false
		}
		p := map[string]any{}
		for m, raw := range key {
			if !jwkCommon[m] && !jwkPublic[kty][m] {
				loc := "keys[][key]"
				if jwkPrivate[m] {
					loc = "keys[]." + m
				}
				rec.add(RuleJWKS, loc, 1)
				continue
			}
			v, ok := stringOrStrings(raw)
			if !ok {
				return "", false
			}
			p[m] = v
		}
		out = append(out, p)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"keys": out}); err != nil {
		return "", false
	}
	return string(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), true
}

// stringOrStrings decodes a string or an array of strings. encoding/json
// takes null as an empty string, so null is refused at both levels.
func stringOrStrings(raw json.RawMessage) (any, bool) {
	if s, ok := jsonString(raw); ok {
		return s, true
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil || elems == nil {
		return nil, false
	}
	ss := make([]string, len(elems))
	for i, e := range elems {
		s, ok := jsonString(e)
		if !ok {
			return nil, false
		}
		ss[i] = s
	}
	return ss, true
}

// jsonString decodes raw when it is a JSON string, not null.
func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '"' || json.Unmarshal(t, &s) != nil {
		return "", false
	}
	return s, true
}

// jwks applies JWKS to an inline JWKS string, or to base64 inline_bytes.
func (w *walker) jwks(n *node, at string, b64 bool) {
	in := n.str
	if b64 {
		d, err := base64.StdEncoding.DecodeString(in)
		if err != nil {
			w.set(n, RuleJWKS, at)
			return
		}
		in = string(d)
	}
	out, fs := JWKS(in)
	for _, f := range fs {
		w.rec.add(f.Rule, joinLoc(at, f.Location), f.Count)
	}
	if b64 && out != Marker {
		out = base64.StdEncoding.EncodeToString([]byte(out))
	}
	n.str = out
}
