package redact

import (
	"regexp"
	"strings"
)

// Rule names a redaction rule in a Finding and in redactions.json.
type Rule string

// Rules, design §7.
const (
	// RuleManagedFields dropped metadata.managedFields.
	RuleManagedFields Rule = "managed-fields"
	// RuleLastApplied dropped the last-applied-configuration annotation.
	RuleLastApplied Rule = "last-applied"
	// RuleHeaderValue redacted a header value whatever the header's name.
	RuleHeaderValue Rule = "header-value"
	// RuleDebugHeader redacted a value in an Envoy debug header line.
	RuleDebugHeader Rule = "debug-header"
	// RuleHeaderPair redacted the value of a pair with a sensitive name.
	RuleHeaderPair Rule = "header-pair"
	// RuleJWKS reduced a JWKS to its public projection, or redacted it whole.
	RuleJWKS Rule = "jwks"
	// RuleKeyName redacted a value whose key names a credential.
	RuleKeyName Rule = "key-name"
	// RuleField redacted a per-kind field: basicAuth hashes, env values, a DirectResponse body.
	RuleField Rule = "field"
	// RuleInline redacted an Envoy inline_string or inline_bytes.
	RuleInline Rule = "inline"
	// RuleLua redacted Lua source.
	RuleLua Rule = "lua"
	// RulePattern redacted a secret-shaped match in free text.
	RulePattern Rule = "pattern"
	// RulePEMTail redacted a private-key block that never ended.
	RulePEMTail Rule = "pem-tail"
)

// sensitiveKeyRe is the key-name rule: a credential word at the end of a key,
// after lowercasing and dropping '_', '-' and '.'.
var sensitiveKeyRe = regexp.MustCompile(`(password|passwd|secret|token|apikey|credential|privatekey|accesskey|secretkey|signature|licensekey)s?$`)

// sensitiveKey reports whether a key or name falls under the key-name rule.
func sensitiveKey(k string) bool {
	k = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == '.' {
			return -1
		}
		return r
	}, strings.ToLower(k))
	return sensitiveKeyRe.MatchString(k)
}

// sensitiveHeaders is the header-pair rule's list, design §7.
var sensitiveHeaders = map[string]bool{
	"authorization":        true,
	"proxy-authorization":  true,
	"cookie":               true,
	"set-cookie":           true,
	"x-api-key":            true,
	"api-key":              true,
	"x-auth-token":         true,
	"x-amz-security-token": true,
}

// sensitiveHeader reports whether a pair's name makes its value secret.
func (o Options) sensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	if sensitiveHeaders[n] || sensitiveKey(n) {
		return true
	}
	for _, h := range o.ExtraHeaders {
		if strings.EqualFold(h, n) {
			return true
		}
	}
	return false
}

// keptDebugHeaders are the Envoy debug header lines whose values stay.
var keptDebugHeaders = map[string]bool{
	":authority":     true,
	":method":        true,
	":scheme":        true,
	":status":        true,
	":path":          true, // gets the pattern pass like every kept value
	"content-type":   true,
	"content-length": true,
	"user-agent":     true,
	"x-request-id":   true,
}

func keptDebugHeader(name string) bool {
	n := strings.ToLower(name)
	return keptDebugHeaders[n] || strings.HasPrefix(n, "x-envoy-")
}

// keptEnv are env var names whose literal values stay (still pattern-passed).
var keptEnv = map[string]bool{
	"LOG_LEVEL":     true,
	"GOMAXPROCS":    true,
	"GOMEMLIMIT":    true,
	"GODEBUG":       true,
	"POD_NAME":      true,
	"POD_NAMESPACE": true,
	"POD_IP":        true,
	"NODE_NAME":     true,
	"HOST_IP":       true,
	"ENVOY_UID":     true,
	"TZ":            true,
}

func keptEnvName(name string) bool { return keptEnv[name] || strings.HasPrefix(name, "KGW_") }
