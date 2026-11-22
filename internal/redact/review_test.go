package redact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

func TestPrometheusWhitespaceGrammar(t *testing.T) {
	const tok = "kgwctl-canary-prom-ws"
	for name, in := range map[string]string{
		"tab before value":   "m{l=\t\"Bearer " + tok + "\"} 1\n",
		"blanks around =":    "m{l \t=  \"Bearer " + tok + "\"} 1\n",
		"indented HELP":      "   # HELP m Bearer " + tok + "\nm 1\n",
		"tab-indented HELP":  "\t# HELP m Bearer " + tok + "\nm 1\n",
		"tab after # HELP":   "#\tHELP\tm Bearer " + tok + "\nm 1\n",
		"indented comment":   "  # note Bearer " + tok + "\nm 1\n",
		"indented sample":    "  m{l=\"Bearer " + tok + "\"} 1\n",
		"quoted name inside": "{\"m.x\", l=\t\"Bearer " + tok + "\"} 1\n",
		"brace in name":      "\"m{x\"{l=\"Bearer " + tok + "\"} 1\n",
		"brace in label":     "m{\"l{\"=\"Bearer " + tok + "\"} 1\n",
		"HELP without blank": "#HELP m Bearer " + tok + "\nm 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := expfmt.NewTextParser(model.UTF8Validation)
			if _, err := p.TextToMetricFamilies(strings.NewReader(in)); err != nil {
				t.Skipf("expfmt rejects the input: %v", err)
			}
			out, fs, err := redact.Prometheus([]byte(in), redact.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), tok) {
				t.Errorf("token kept:\n%s", out)
			}
			if !hasFinding(fs, redact.RulePattern, "line:1") {
				t.Errorf("findings %+v", fs)
			}
			p = expfmt.NewTextParser(model.UTF8Validation)
			if _, err := p.TextToMetricFamilies(bytes.NewReader(out)); err != nil {
				t.Errorf("output does not parse: %v\n%s", err, out)
			}
		})
	}
}

func TestDebugHeaderJSONEscapedBackslash(t *testing.T) {
	in := `{"msg":"headers:\n'x-tenant-key', 'kgwctl-canary-esc\\nsuffix'"}` + "\n"
	var w lines
	res, err := redact.Text(strings.NewReader(in), &w, 1<<20, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"msg":"headers:\n'x-tenant-key', '[redacted]'"}`
	if got := w.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !hasFinding(res.Findings, redact.RuleDebugHeader, "line:1") {
		t.Errorf("findings %+v", res.Findings)
	}
}

func TestBasicAnyCase(t *testing.T) {
	for _, in := range []string{
		"authorization: basic dXNlcjpwYXNz",
		"authorization: Basic dXNlcjpwYXNz",
		"authorization: BASIC dXNlcjpwYXNz",
	} {
		got := redact.ErrorText(errors.New(in), "")
		if strings.Contains(got, "dXNlcjpwYXNz") || !strings.HasSuffix(got, " [redacted]") {
			t.Errorf("%q gave %q", in, got)
		}
	}
	// Prose stays.
	if got := redact.ErrorText(errors.New("basic authentication failed"), ""); got != "basic authentication failed" {
		t.Errorf("prose changed: %q", got)
	}
}

func TestQuotedAssignment(t *testing.T) {
	cases := map[string]string{
		`login failed: password="dummy two words" user=a`:     `login failed: password="[redacted]" user=a`,
		`login failed: password='dummy two words' user=a`:     `login failed: password='[redacted]' user=a`,
		`{"client_secret":"dummy two words","user":"a"}`:      `{"client_secret":"[redacted]","user":"a"}`,
		`{"apiKey":"a\"b c","x":1}`:                           `{"apiKey":"[redacted]","x":1}`,
		`err: {\"access_token\":\"dummy two words\",\"x\":1}`: `err: {\"access_token\":\"[redacted]\",\"x\":1}`,
		`token: "dummy two words"`:                            `token: "[redacted]"`,
		// An escaped quote inside an escaped JSON value, and a pattern-free variant
		// with an escaped backslash and \n before it.
		`err: {\"access_token\":\"dummy\\\"two words\",\"x\":1}`: `err: {\"access_token\":\"[redacted]\",\"x\":1}`,
		`err: {\"client_secret\":\"p\\\\q\\nr\\\"plain tail\"}`:  `err: {\"client_secret\":\"[redacted]\"}`,
	}
	for in, want := range cases {
		var w lines
		if _, err := redact.Text(strings.NewReader(in+"\n"), &w, 1<<20, redact.Options{}); err != nil {
			t.Fatal(err)
		}
		if got := w.String(); got != want {
			t.Errorf("text %s\n got  %s\n want %s", in, got, want)
		}
		if got := redact.ErrorText(errors.New(in), ""); got != want {
			t.Errorf("error text %s\n got  %s\n want %s", in, got, want)
		}
	}
}

func TestObjectListItemKinds(t *testing.T) {
	const list = `{"apiVersion":"v1","kind":"List","metadata":{},"items":[
		{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"DirectResponse","metadata":{"name":"dr-l"},
			"spec":{"status":200,"body":"plainbodyvalue1"}},
		{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"ListenerPolicy","metadata":{"name":"lp-l"},
			"spec":{"default":{"httpSettings":{"headers":[{"name":"x-tenant","value":"plainhdrvalue2"}]}}}}]}`
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON([]byte(list)); err != nil {
		t.Fatal(err)
	}
	fs, err := redact.Object(u, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(u.Object)
	for _, s := range []string{"plainbodyvalue1", "plainhdrvalue2"} {
		if strings.Contains(string(b), s) {
			t.Errorf("%q kept:\n%s", s, b)
		}
	}
	for _, s := range []string{"dr-l", "lp-l", "x-tenant"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("%q lost:\n%s", s, b)
		}
	}
	if !hasFinding(fs, redact.RuleField, "items[].spec.body") ||
		!hasFinding(fs, redact.RuleHeaderValue, "items[].spec.default.httpSettings.headers[].value") {
		t.Errorf("findings %+v", fs)
	}
}

func TestScalarUnderCredentialKey(t *testing.T) {
	const in = `{"password":123456,"auth":{"token":true},"tokens":[42],"port":8080,"secretName":7,"name":null,
		"hdr":{"name":"x-api-key","value":98765}}`
	check := func(t *testing.T, out []byte, fs []redact.Finding) {
		t.Helper()
		for _, s := range []string{"123456", "true", "42", "98765"} {
			if strings.Contains(string(out), s) {
				t.Errorf("%s kept:\n%s", s, out)
			}
		}
		for _, s := range []string{"8080", `"secretName": 7`, `"name": null`} {
			if !strings.Contains(string(out), s) {
				t.Errorf("%s lost:\n%s", s, out)
			}
		}
		if !hasFinding(fs, redact.RuleKeyName, "password") {
			t.Errorf("findings %+v", fs)
		}
	}
	t.Run("json", func(t *testing.T) {
		out, fs, err := redact.JSON([]byte(in), redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		check(t, out, fs)
	})
	t.Run("config-dump", func(t *testing.T) {
		out, fs, err := redact.ConfigDump([]byte(in), redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		check(t, out, fs)
	})
	t.Run("object", func(t *testing.T) {
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON([]byte(`{"apiVersion":"v1","kind":"X","metadata":{"name":"x"},"spec":{"password":123456}}`)); err != nil {
			t.Fatal(err)
		}
		fs, err := redact.Object(u, redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "password"); v != redact.Marker {
			t.Errorf("spec.password = %v", v)
		}
		if !hasFinding(fs, redact.RuleKeyName, "spec.password") {
			t.Errorf("findings %+v", fs)
		}
	})
}

func TestMapKeysMaskedInLocations(t *testing.T) {
	u := objectFromYAML(t, []byte(`
apiVersion: v1
kind: Service
metadata: {name: svc}
spec:
  selector:
    teamblue: "Bearer dummy-token"
  myCustomField:
    nested: "Bearer dummy-token2"
`))
	fs, err := redact.Object(u, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(fs, redact.RulePattern, "spec.selector[key]") || !hasFinding(fs, redact.RulePattern, "spec[key][key]") {
		t.Errorf("findings %+v", fs)
	}
	dump := `{"configs":[{"bootstrap":{"node":{"id":"n","metadata":{"teamblue":"Bearer dummy-token"}}}}]}`
	_, cfs, err := redact.ConfigDump([]byte(dump), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(cfs, redact.RulePattern, "configs[].bootstrap.node.metadata[key]") {
		t.Errorf("config dump findings %+v", cfs)
	}
	for _, f := range append(fs, cfs...) {
		for _, k := range []string{"teamblue", "myCustomField", "nested"} {
			if strings.Contains(f.Location, k) {
				t.Errorf("location %q holds key %q", f.Location, k)
			}
		}
	}
}

func TestJWKSNullMembers(t *testing.T) {
	for name, in := range map[string]string{
		"null n":           `{"keys":[{"kty":"RSA","n":null,"e":"AQAB"}]}`,
		"null key_ops":     `{"keys":[{"kty":"RSA","n":"nnn","e":"AQAB","key_ops":[null]}]}`,
		"mixed key_ops":    `{"keys":[{"kty":"RSA","n":"nnn","e":"AQAB","key_ops":["verify",null]}]}`,
		"null kid":         `{"keys":[{"kty":"EC","kid":null,"crv":"P-256","x":"x","y":"y"}]}`,
		"number in x5c":    `{"keys":[{"kty":"RSA","n":"nnn","e":"AQAB","x5c":[1]}]}`,
		"null keys member": `{"keys":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, fs := redact.JWKS(in)
			if out != redact.Marker {
				t.Errorf("got %q, want whole [redacted]", out)
			}
			if len(fs) != 1 || fs[0].Rule != redact.RuleJWKS || fs[0].Location != "" {
				t.Errorf("findings %+v", fs)
			}
		})
	}
}
