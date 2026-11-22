package redact

// ConfigDump redacts an Envoy /config_dump body (design §7). Envoy's own
// [redacted] values stay. Header values under the Envoy header structures,
// Lua sources and every other inline_string and inline_bytes are redacted,
// local_jwks gets the JWKS projection, and a JSON StringValue such as the
// dynamic-module config is parsed and redacted inside. Then the key-name and
// header-pair rules and the pattern pass. Invalid JSON gives ErrInvalidJSON
// and no output.
func ConfigDump(body []byte, o Options) ([]byte, []Finding, error) {
	return redactTree(body, modeConfigDump, o)
}

// JSON redacts a JSON document with the key-name and header-pair rules and
// the pattern pass. Invalid JSON gives ErrInvalidJSON and no output.
func JSON(body []byte, o Options) ([]byte, []Finding, error) {
	return redactTree(body, modeJSON, o)
}

func redactTree(body []byte, m mode, o Options) ([]byte, []Finding, error) {
	t, err := parseTree(body)
	if err != nil {
		return nil, nil, err
	}
	w := &walker{mode: m, opts: o, rec: &recorder{}}
	w.walk(t, "", "", 0)
	return t.indented(o.MaxSize), w.rec.findings(), nil
}
