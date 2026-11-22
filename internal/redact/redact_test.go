package redact_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

// hv is a header value no pattern matches, so only the header-value rule can catch it.
const hv = "hvplainvalue42"

func TestHeaderValueAnyName(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte(hv))
	for _, name := range []string{"x-tenant-key", "x-foo", "Authorization"} {
		t.Run(name, func(t *testing.T) {
			dump := fmt.Sprintf(`{"configs":[{"route_config":{
				"request_headers_to_add":[{"header":{"key":%[1]q,"value":%[2]q}}],
				"response_headers_to_add":[{"header":{"key":%[1]q,"raw_value":%[3]q}}],
				"ext_authz":{"http_service":{"authorization_request":{"headers_to_add":[{"key":%[1]q,"value":%[2]q}]}},
					"grpc_service":{"initial_metadata":[{"key":%[1]q,"value":%[2]q}]}},
				"header_mutation":{"mutations":{
					"request_mutations":[{"append":{"header":{"key":%[1]q,"value":%[2]q}}}],
					"response_mutations":[{"append":{"header":{"key":%[1]q,"value":%[2]q}}}]}},
				"tunneling_config":{"hostname":"proxy.example.com:443","headers_to_add":[{"header":{"key":%[1]q,"value":%[2]q}}]}
			}}]}`, name, hv, raw)
			out, fs, err := redact.ConfigDump([]byte(dump), redact.Options{})
			if err != nil {
				t.Fatal(err)
			}
			checkHeaderValues(t, string(out), name, fs, 7)
			if strings.Contains(string(out), raw) {
				t.Error("raw_value kept")
			}

			objects := []string{
				`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute","metadata":{"name":"r"},"spec":{"rules":[{"filters":[
					{"requestHeaderModifier":{"set":[{"name":%[1]q,"value":%[2]q}],"add":[{"name":%[1]q,"value":%[2]q}]}},
					{"responseHeaderModifier":{"set":[{"name":%[1]q,"value":%[2]q}]}}]}]}}`,
				`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"GRPCRoute","metadata":{"name":"g"},"spec":{"rules":[{"filters":[
					{"requestHeaderModifier":{"add":[{"name":%[1]q,"value":%[2]q}]}}]}]}}`,
				`{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"TrafficPolicy","metadata":{"name":"t"},"spec":{"headerModifiers":{
					"request":{"set":[{"name":%[1]q,"value":%[2]q}]},"response":{"add":[{"name":%[1]q,"value":%[2]q}]}}}}`,
				`{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"GatewayExtension","metadata":{"name":"e"},"spec":{"extAuth":{"httpService":{
					"authorizationRequest":{"headersToAdd":{%[1]q:%[2]q}}}}}}`,
				`{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"BackendConfigPolicy","metadata":{"name":"b"},"spec":{"tunnel":{
					"headers":[{"name":%[1]q,"value":%[2]q}]}}}`,
				`{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"ListenerPolicy","metadata":{"name":"l"},"spec":{"default":{
					"accessLog":[{"grpcService":{"initialMetadata":[{"key":%[1]q,"value":%[2]q}]}}]}}}`,
				`{"apiVersion":"gateway.kgateway.dev/v1alpha1","kind":"ListenerPolicy","metadata":{"name":"l"},"spec":{"default":{
					"httpSettings":{"earlyRequestHeaderModifier":{"set":[{"name":%[1]q,"value":%[2]q}],"add":[{"name":%[1]q,"value":%[2]q}]}}}}}`,
			}
			for _, tmpl := range objects {
				u := &unstructured.Unstructured{}
				if err := u.UnmarshalJSON([]byte(fmt.Sprintf(tmpl, name, hv))); err != nil {
					t.Fatal(err)
				}
				fs, err := redact.Object(u, redact.Options{})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(u.Object)
				checkHeaderValues(t, string(b), name, fs, 1)
				if u.GetKind() == "GatewayExtension" {
					if len(fs) != 1 || !strings.HasSuffix(fs[0].Location, "headersToAdd[key]") {
						t.Errorf("GatewayExtension findings %+v", fs)
					}
				}
			}
		})
	}
}

func checkHeaderValues(t *testing.T, out, name string, fs []redact.Finding, minFindings int) {
	t.Helper()
	if strings.Contains(out, hv) {
		t.Errorf("header value kept: %s", out)
	}
	if !strings.Contains(out, name) {
		t.Errorf("header name %q lost: %s", name, out)
	}
	n := 0
	for _, f := range fs {
		if f.Rule != redact.RuleHeaderValue {
			t.Errorf("unexpected finding %+v", f)
		}
		n += f.Count
	}
	if n < minFindings {
		t.Errorf("%d header-value findings, want at least %d: %+v", n, minFindings, fs)
	}
}

func TestDebugHeaderLines(t *testing.T) {
	in := strings.Join([]string{
		"'x-tenant-key', 'v'",
		"'authorization', 'Bearer v'",
		"':method', 'GET'",
		"'x-envoy-upstream-service-time', '3'",
		"':path', '/a?token=v'",
		"2026-11-22T10:00:00.000000000Z 'x-tenant-key', 'v'",
		"2026-11-22T11:00:00+01:00 ':method', 'GET'",
		`{"msg":"request headers complete:\n':method', 'GET'\n'x-tenant-key', 'v'\n'cookie', 'a=v'"}`,
	}, "\n") + "\n"
	var w lines
	res, err := redact.Text(strings.NewReader(in), &w, 1<<20, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"'x-tenant-key', '[redacted]'",
		"'authorization', '[redacted]'",
		"':method', 'GET'",
		"'x-envoy-upstream-service-time', '3'",
		"':path', '/a?token=[redacted]'",
		"2026-11-22T10:00:00.000000000Z 'x-tenant-key', '[redacted]'",
		"2026-11-22T11:00:00+01:00 ':method', 'GET'",
		`{"msg":"request headers complete:\n':method', 'GET'\n'x-tenant-key', '[redacted]'\n'cookie', '[redacted]'"}`,
	}
	if !reflect.DeepEqual(w.l, want) {
		t.Errorf("got %q\nwant %q", w.l, want)
	}
	if !hasFinding(res.Findings, redact.RuleDebugHeader, "line:1") || !hasFinding(res.Findings, redact.RuleDebugHeader, "line:2") ||
		!hasFinding(res.Findings, redact.RulePattern, "line:5") || !hasFinding(res.Findings, redact.RuleDebugHeader, "line:6") ||
		!hasFinding(res.Findings, redact.RuleDebugHeader, "line:8") {
		t.Errorf("findings %+v", res.Findings)
	}
}

func TestKeyNamePluralAndNested(t *testing.T) {
	in := `{"apiKeys":["plainkey1"],"tokens":["plaintok2"],"password":{"value":"plainpw3"},
		"secret":{"name":"keepname","value":"plainsv4"},"clientSecrets":[{"name":"keepname2","data":"plaincs5"}]}`
	out, fs, err := redact.JSON([]byte(in), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"plainkey1", "plaintok2", "plainpw3", "plainsv4", "plaincs5"} {
		if strings.Contains(string(out), s) {
			t.Errorf("%q kept:\n%s", s, out)
		}
	}
	for _, s := range []string{"keepname", "keepname2"} {
		if !strings.Contains(string(out), s) {
			t.Errorf("reference name %q lost:\n%s", s, out)
		}
	}
	// apiKeys, tokens and clientSecrets are not schema fields, so they show as [key].
	for _, loc := range []string{"[key][]", "password.value", "secret.value", "[key][].data"} {
		if !hasFinding(fs, redact.RuleKeyName, loc) {
			t.Errorf("no key-name finding at %q: %+v", loc, fs)
		}
	}
}

func TestEscapedJSONAssignment(t *testing.T) {
	in := `{"level":"error","error":"token exchange failed: {\"access_token\":\"opaqueValue123\"}"}` + "\n"
	var w lines
	if _, err := redact.Text(strings.NewReader(in), &w, 1<<20, redact.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := w.String(); strings.Contains(got, "opaqueValue123") || !strings.Contains(got, `\"access_token\":\"[redacted]\"}`) {
		t.Errorf("got %s", got)
	}
}

func TestJWKSProjection(t *testing.T) {
	const rsa = `{"kty":"RSA","kid":"r1","use":"sig","alg":"RS256","n":"nnn","e":"AQAB","key_ops":["verify"],"x5c":["MIIC"],"x5t":"t1","x5t#S256":"t2"}`
	const ec = `{"kty":"EC","kid":"e1","crv":"P-256","x":"xx","y":"yy"}`
	const okp = `{"kty":"OKP","kid":"o1","crv":"Ed25519","x":"ox"}`
	t.Run("projection", func(t *testing.T) {
		in := `{"keys":[` +
			strings.TrimSuffix(rsa, "}") + `,"d":"priv-d","k":"sym-k","extra":"x-member"},` +
			ec + `,` + okp + `],"other":"top-member"}`
		out, fs := redact.JWKS(in)
		var got map[string][]map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 {
			t.Fatalf("not a key set: %q (%v)", out, err)
		}
		var want map[string][]map[string]any
		_ = json.Unmarshal([]byte(`{"keys":[`+rsa+`,`+ec+`,`+okp+`]}`), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %s", out)
		}
		for _, s := range []string{"priv-d", "sym-k", "x-member", "top-member", "extra", "other"} {
			if strings.Contains(out, s) {
				t.Errorf("%q kept", s)
			}
		}
		for _, loc := range []string{"keys[].d", "keys[].k", "keys[][key]", "[key]"} {
			if !hasFinding(fs, redact.RuleJWKS, loc) {
				t.Errorf("no finding at %q: %+v", loc, fs)
			}
		}
	})
	for name, in := range map[string]string{
		"nested n":     `{"keys":[{"kty":"RSA","n":{"d":"priv"},"e":"AQAB"}]}`,
		"number kid":   `{"keys":[{"kty":"RSA","kid":7,"n":"nnn","e":"AQAB"}]}`,
		"oct":          `{"keys":[{"kty":"oct","k":"sym"}]}`,
		"wrong shape":  `["priv"]`,
		"keys object":  `{"keys":{"kty":"RSA"}}`,
		"invalid json": `{"keys":[priv`,
		"no kty":       `{"keys":[{"n":"nnn","e":"AQAB"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, fs := redact.JWKS(in)
			if out != "[redacted]" {
				t.Errorf("got %q, want whole [redacted]", out)
			}
			if len(fs) != 1 || fs[0].Rule != redact.RuleJWKS || fs[0].Location != "" {
				t.Errorf("findings %+v", fs)
			}
		})
	}
}

func TestPrometheus(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJlLXZhbHVl"
	in := "# HELP envoy_cluster_upstream_rq Requests. token " + jwt + "\n" +
		"# TYPE envoy_cluster_upstream_rq counter\n" +
		`envoy_cluster_upstream_rq{envoy_cluster_name="kube_team-a_svc-a_8080",tok="` + jwt + `"} 7` + "\n"
	out, fs, err := redact.Prometheus([]byte(in), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, jwt) {
		t.Errorf("JWT kept: %s", s)
	}
	for _, k := range []string{"envoy_cluster_upstream_rq{", `envoy_cluster_name="kube_team-a_svc-a_8080"`, `tok="[redacted]"`, "# TYPE envoy_cluster_upstream_rq counter", "} 7"} {
		if !strings.Contains(s, k) {
			t.Errorf("%q missing: %s", k, s)
		}
	}
	if !hasFinding(fs, redact.RulePattern, "line:1") || !hasFinding(fs, redact.RulePattern, "line:3") {
		t.Errorf("findings %+v", fs)
	}
	out, _, err = redact.Prometheus([]byte("<html><body>login</body></html>\n"), redact.Options{})
	if !errors.Is(err, redact.ErrNotPrometheus) || out != nil {
		t.Errorf("HTML: out %q, err %v", out, err)
	}
}

func TestTextCutAndCancel(t *testing.T) {
	t.Run("reader fails mid-line", func(t *testing.T) {
		var w lines
		res, err := redact.Text(io.MultiReader(strings.NewReader("a\nb\npartial-line"), failReader{}), &w, 1<<20, redact.Options{})
		if err == nil || !res.Truncated || res.Reason != redact.ReasonReadError {
			t.Errorf("err %v, result %+v", err, res)
		}
		if !reflect.DeepEqual(w.l, []string{"a", "b"}) {
			t.Errorf("lines %q", w.l)
		}
	})
	t.Run("open PEM block", func(t *testing.T) {
		var w lines
		res, err := redact.Text(strings.NewReader("x\n-----BEGIN EC PRIVATE KEY-----\nAAAA\nBBBB\n"), &w, 1<<20, redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"x", "-----BEGIN EC PRIVATE KEY-----", "[redacted]", "[redacted]"}
		if !reflect.DeepEqual(w.l, want) {
			t.Errorf("lines %q", w.l)
		}
		if !hasFinding(res.Findings, redact.RulePEMTail, "line:2") {
			t.Errorf("findings %+v", res.Findings)
		}
	})
	t.Run("cap at a line boundary", func(t *testing.T) {
		var w lines
		res, err := redact.Text(strings.NewReader("aaaa\nbbbb\ncccc\n"), &w, 12, redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(w.l, []string{"aaaa", "bbbb"}) {
			t.Errorf("lines %q", w.l)
		}
		if !res.Truncated || res.Reason != redact.ReasonSizeCap {
			t.Errorf("result %+v", res)
		}
	})
	t.Run("last line without newline", func(t *testing.T) {
		var w lines
		res, err := redact.Text(strings.NewReader("a\nb"), &w, 1<<20, redact.Options{})
		if err != nil || res.Truncated || !reflect.DeepEqual(w.l, []string{"a", "b"}) {
			t.Errorf("err %v, result %+v, lines %q", err, res, w.l)
		}
	})
}

func TestRemoteError(t *testing.T) {
	const canary = "kgwctl-canary-remote"
	err := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "gw", errors.New(canary))
	if got := redact.RemoteError(err); got != "403 Forbidden (Forbidden)" {
		t.Errorf("got %q", got)
	}
	logErr := fmt.Errorf("%d while accessing %v: %s", 502, "https://10.0.0.1:6443/api/v1/namespaces/a/pods/p/log", canary)
	if got := redact.RemoteError(fmt.Errorf("stream logs: %w", logErr)); got != "502 Bad Gateway" {
		t.Errorf("got %q", got)
	}
	if got := redact.RemoteError(errors.New(canary)); strings.Contains(got, canary) {
		t.Errorf("unknown error kept its text: %q", got)
	}
}

func TestErrorText(t *testing.T) {
	err := errors.New(`Get "https://api.prod.example.com:6443/api/v1/pods?x=1": dial to api.prod.example.com failed; ` +
		`Authorization: Bearer abc.def-ghi; key sk-abcdefghijklmnopqrstuvwx`)
	got := redact.ErrorText(err, "https://api.prod.example.com:6443")
	for _, s := range []string{"api.prod.example.com", "/api/v1/pods", "abc.def-ghi", "abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(got, s) {
			t.Errorf("%q kept in %q", s, got)
		}
	}
	if !strings.Contains(got, "<apiserver>") || !strings.Contains(got, "Bearer [redacted]") {
		t.Errorf("got %q", got)
	}
	long := redact.ErrorText(errors.New(strings.Repeat("é", 1024)), "")
	if len(long) > 512 || !strings.HasPrefix(strings.Repeat("é", 256), long) || len(long) < 500 {
		t.Errorf("cut to %d bytes", len(long))
	}
	if redact.ErrorText(nil, "") != "" {
		t.Error("nil error gave text")
	}
}

func TestObjectForbiddenKinds(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": "x"}, "data": map[string]any{"k": "v"},
		}}
		if _, err := redact.Object(u, redact.Options{}); !errors.Is(err, redact.ErrForbiddenKind) {
			t.Errorf("%s: err %v", kind, err)
		}
	}
	list := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "List", "items": []any{map[string]any{"apiVersion": "v1", "kind": "Secret"}},
	}}
	if _, err := redact.Object(list, redact.Options{}); !errors.Is(err, redact.ErrForbiddenKind) {
		t.Errorf("list: err %v", err)
	}
}

func TestKeepsUseful(t *testing.T) {
	t.Run("object", func(t *testing.T) {
		const route = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: route-a
  namespace: team-a
  labels: {app: web}
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: "{}"
spec:
  parentRefs: [{name: gw, namespace: kgateway-system, sectionName: https}]
  hostnames: [api.example.com]
  rules:
    - matches: [{headers: [{name: x-version, value: v2}], path: {type: PathPrefix, value: /v1}}]
      filters:
        - type: RequestHeaderModifier
          requestHeaderModifier:
            set: [{name: Authorization, value: Bearer abc}]
            remove: [x-internal]
      backendRefs: [{name: svc-a, port: 8080}]
`
		u := objectFromYAML(t, []byte(route))
		before := keySet(u.Object)
		if _, err := redact.Object(u, redact.Options{}); err != nil {
			t.Fatal(err)
		}
		delete(before, "metadata.annotations.kubectl.kubernetes.io/last-applied-configuration")
		if after := keySet(u.Object); !reflect.DeepEqual(before, after) {
			t.Errorf("key set changed:\nbefore %v\nafter  %v", sorted(before), sorted(after))
		}
		y, _ := yaml.Marshal(u.Object)
		for _, k := range []string{"route-a", "team-a", "kgateway-system", "api.example.com", "Authorization", "x-version", "x-internal", "svc-a", "/v1", "v2", "app: web"} {
			if !strings.Contains(string(y), k) {
				t.Errorf("%q lost:\n%s", k, y)
			}
		}
		if u.GetName() != "route-a" || u.GetNamespace() != "team-a" {
			t.Error("name or namespace changed")
		}
		if _, err := yaml.YAMLToJSON(y); err != nil {
			t.Errorf("redacted YAML does not re-parse: %v", err)
		}
	})
	t.Run("config dump", func(t *testing.T) {
		in := `{"configs":[{"@type":"type.googleapis.com/envoy.admin.v3.ClustersConfigDump","dynamic_active_clusters":[{"cluster":{
			"name":"kube_team-a_svc-a_8080","type":"EDS","connect_timeout":"5s",
			"transport_socket":{"typed_config":{"sni":"api.example.com","common_tls_context":{"tls_certificates":[{"private_key":{"inline_bytes":"W3JlZGFjdGVkXQ=="}}]}}}}}]},
			{"@type":"type.googleapis.com/envoy.admin.v3.ListenersConfigDump","dynamic_listeners":[{"name":"l","active_state":{"listener":{"filter_chains":[{"filters":[{"typed_config":{
			"http_filters":[{"name":"envoy.filters.http.jwt_authn","typed_config":{"providers":{"p":{"local_jwks":{"inline_string":"{\"keys\":[{\"kty\":\"EC\",\"crv\":\"P-256\",\"x\":\"pubx\",\"y\":\"puby\",\"kid\":\"k1\"}]}"}}}}}],
			"request_headers_to_add":[{"header":{"key":"x-trace","value":"abc"}}]}}]}]}}}]}]}`
		out, _, err := redact.ConfigDump([]byte(in), redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		_ = json.Unmarshal([]byte(in), &a)
		if err := json.Unmarshal(out, &b); err != nil {
			t.Fatalf("output does not re-parse: %v", err)
		}
		if !reflect.DeepEqual(keySet(a), keySet(b)) {
			t.Errorf("key set changed")
		}
		for _, k := range []string{"kube_team-a_svc-a_8080", "api.example.com", "W3JlZGFjdGVkXQ==", "x-trace", `\"x\":\"pubx\"`, `\"kid\":\"k1\"`, `\"crv\":\"P-256\"`, "5s"} {
			if !strings.Contains(string(out), k) {
				t.Errorf("%q lost:\n%s", k, out)
			}
		}
	})
	t.Run("token bucket", func(t *testing.T) {
		in := `{"configs":[{"dynamic_listeners":[{"active_state":{"listener":{"filter_chains":[{"filters":[{"typed_config":{"http_filters":[{"name":"envoy.filters.http.local_ratelimit","typed_config":{
			"stat_prefix":"http_local_rate_limiter","token_bucket":{"max_tokens":1000,"tokens_per_fill":100,"fill_interval":"1s"}}}]}}]}]}}}]}]}`
		out, fs, err := redact.ConfigDump([]byte(in), redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(fs) != 0 {
			t.Errorf("findings %+v", fs)
		}
		for _, k := range []string{`"max_tokens": 1000`, `"tokens_per_fill": 100`, `"fill_interval": "1s"`} {
			if !strings.Contains(string(out), k) {
				t.Errorf("%q lost:\n%s", k, out)
			}
		}
	})
	t.Run("certs json", func(t *testing.T) {
		in := `{"certificates":[{"ca_cert":[{"path":"/etc/envoy/ca.crt","serial_number":"0a1b","subject_alt_names":[{"dns":"api.example.com"},{"uri":"spiffe://cluster.local/ns/team-a/sa/web"}],"days_until_expiration":"30","valid_from":"2026-01-01T00:00:00Z","expiration_time":"2026-12-22T00:00:00Z"}]}]}`
		out, fs, err := redact.JSON([]byte(in), redact.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(fs) != 0 {
			t.Errorf("findings %+v", fs)
		}
		var a, b any
		_ = json.Unmarshal([]byte(in), &a)
		_ = json.Unmarshal(out, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("certs changed:\n%s", out)
		}
	})
}

// keySet lists every key path of v, with array elements as [].
func keySet(v any) map[string]bool {
	s := map[string]bool{}
	var walk func(p string, v any)
	walk = func(p string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, e := range v {
				q := p + "." + k
				s[q] = true
				walk(q, e)
			}
		case []any:
			for _, e := range v {
				walk(p+"[]", e)
			}
		}
	}
	walk("", v)
	out := map[string]bool{}
	for k := range s {
		out[strings.TrimPrefix(k, ".")] = true
	}
	return out
}

func sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
