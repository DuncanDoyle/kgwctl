package redact_test

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

func TestBasicInAuthorizationContext(t *testing.T) {
	for _, in := range []string{
		"authorization: basic ajpqampq",
		"Authorization: Basic ajpqampq",
		"proxy-authorization=BASIC ajpqampq",
		`"authorization":"basic ajpqampq"`,
	} {
		var w lines
		if _, err := redact.Text(strings.NewReader(in+"\n"), &w, 0, redact.Options{}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.String(), "ajpqampq") {
			t.Errorf("Text kept the credential: %q", w.String())
		}
		if got := redact.ErrorText(errors.New(in), ""); strings.Contains(got, "ajpqampq") {
			t.Errorf("ErrorText kept the credential: %q", got)
		}
	}
	prose := "the gateway uses basic authentication here"
	if got := redact.ErrorText(errors.New(prose), ""); got != prose {
		t.Errorf("prose changed: %q", got)
	}
}

func TestKeptNamesNotHeaders(t *testing.T) {
	out, fs, err := redact.JSON([]byte(`{"name":"max-tokens","value":"dummy-opaque-value"}`), redact.Options{})
	if err != nil || strings.Contains(string(out), "dummy-opaque-value") || len(fs) != 1 {
		t.Errorf("header pair named max-tokens kept: %s %+v %v", out, fs, err)
	}
	out, _, _ = redact.JSON([]byte(`{"max_tokens":"dummy-opaque-value","tokens_per_fill":100}`), redact.Options{})
	if strings.Contains(string(out), "dummy-opaque-value") || !strings.Contains(string(out), "100") {
		t.Errorf("token-bucket: %s", out)
	}
}

func TestMapFieldKeysMasked(t *testing.T) {
	svc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "s"},
		"spec": map[string]any{"selector": map[string]any{"name": "Bearer dummy-token"}},
	}}
	fs, err := redact.Object(svc, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if strings.Contains(f.Location, "selector.name") {
			t.Errorf("location %q shows the selector key", f.Location)
		}
	}
	dump := `{"configs":[{"bootstrap":{"node":{"metadata":{"name":"Bearer dummy-token"}}}}]}`
	_, fs, err = redact.ConfigDump([]byte(dump), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if strings.Contains(f.Location, "metadata.name") {
			t.Errorf("location %q shows the node metadata key", f.Location)
		}
	}
}

func TestLocationCapped(t *testing.T) {
	const depth = 6000
	doc := strings.Repeat(`{"a":`, depth) + `{"password":"x"}` + strings.Repeat("}", depth)
	var fs []redact.Finding
	var err error
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, fs, err = redact.JSON([]byte(doc), redact.Options{MaxSize: 1 << 20}) // compact: indentation is bounded separately
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 40<<20 {
		t.Errorf("JSON allocated %d bytes", n)
	}
	if len(fs) != 1 || fs[0].Rule != redact.RuleKeyName || len(fs[0].Location) > 512 || !strings.HasSuffix(fs[0].Location, "password") {
		t.Errorf("findings %d, first %+v", len(fs), firstLoc(fs))
	}
}

func firstLoc(fs []redact.Finding) string {
	if len(fs) == 0 {
		return ""
	}
	l := fs[0].Location
	return fmt.Sprintf("%s (%d bytes) ...%s", fs[0].Rule, len(l), l[max(0, len(l)-40):])
}

func TestTextFindingsBounded(t *testing.T) {
	var in bytes.Buffer
	for range 5000 {
		in.WriteString("Bearer x\n")
	}
	var w lines
	res, err := redact.Text(&in, &w, 0, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) > 4096 {
		t.Errorf("Text kept %d findings", len(res.Findings))
	}
}
