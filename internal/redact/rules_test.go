package redact_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

func TestKeyUnderCredentialRedacted(t *testing.T) {
	out, fs, err := redact.JSON([]byte(`{"apiKeys":[{"key":"plainvalue"}]}`), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "plainvalue") {
		t.Errorf("value under apiKeys[].key kept:\n%s", out)
	}
	if !hasFinding(fs, redact.RuleKeyName, "[key][].key") {
		t.Errorf("findings %+v", fs)
	}
}

func TestDebugHeaderEscapedNewlineInValue(t *testing.T) {
	var w lines
	if _, err := redact.Text(strings.NewReader(`'x-foo', 'abc\ndef'`+"\n"), &w, 1<<20, redact.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := w.String(); strings.Contains(got, "abc") || strings.Contains(got, "def") || got != `'x-foo', '[redacted]'` {
		t.Errorf("got %q", got)
	}
}

func TestTextLineFindings(t *testing.T) {
	var w lines
	in := "plain\nAuthorization: Bearer abc.def-ghi\n'cookie', 'a=b'\n"
	res, err := redact.Text(strings.NewReader(in), &w, 1<<20, redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]redact.Finding{
		nil,
		{{Rule: redact.RulePattern, Location: "line:2", Count: 1}},
		{{Rule: redact.RuleDebugHeader, Location: "line:3", Count: 1}},
	}
	if !reflect.DeepEqual(w.fs, want) {
		t.Errorf("per-line findings %+v, want %+v", w.fs, want)
	}
	var all []redact.Finding
	for _, fs := range w.fs {
		all = append(all, fs...)
	}
	if !reflect.DeepEqual(res.Findings, all) {
		t.Errorf("result findings %+v, per-line %+v", res.Findings, all)
	}
}

func TestIsRemote(t *testing.T) {
	noCode := &apierrors.StatusError{ErrStatus: metav1.Status{Message: `{"clientSecret":"plainbody"}`}}
	logErr := fmt.Errorf("%d while accessing %v: body", 500, "https://10.0.0.1:6443/api/v1/namespaces/a/pods/p/log")
	for _, err := range []error{noCode, fmt.Errorf("list: %w", noCode), logErr} {
		if !redact.IsRemote(err) {
			t.Errorf("IsRemote(%v) = false", err)
		}
	}
	if got := redact.RemoteError(noCode); strings.Contains(got, "plainbody") {
		t.Errorf("RemoteError kept the body: %q", got)
	}
	if redact.IsRemote(errors.New("dial tcp: refused")) || redact.IsRemote(nil) {
		t.Error("a local error is remote")
	}
}
