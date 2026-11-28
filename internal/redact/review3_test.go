package redact_test

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
)

func TestDeepListStructuralRules(t *testing.T) {
	var obj any = map[string]any{
		"apiVersion": "gateway.kgateway.dev/v1alpha1", "kind": "DirectResponse",
		"metadata": map[string]any{"name": "d"}, "spec": map[string]any{"body": "dummy-body"},
	}
	// 64 Lists make the displayed location pass its 512-byte cap.
	for range 64 {
		obj = map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{obj}}
	}
	u := &unstructured.Unstructured{Object: obj.(map[string]any)}
	if _, err := redact.Object(u, redact.Options{}); err != nil {
		t.Fatal(err)
	}
	cur := u.Object
	for range 64 {
		cur = cur["items"].([]any)[0].(map[string]any)
	}
	if body := cur["spec"].(map[string]any)["body"]; body != redact.Marker {
		t.Errorf("deep DirectResponse body = %v", body)
	}
}

func TestCredentialZoneWinsOverKeptNames(t *testing.T) {
	out, _, err := redact.JSON([]byte(`{"credentials":{"max_tokens":123456}}`), redact.Options{})
	if err != nil || strings.Contains(string(out), "123456") {
		t.Errorf("max_tokens under credentials kept: %s %v", out, err)
	}
}

func TestNestedMetadataKeysMasked(t *testing.T) {
	dump := `{"configs":[{"bootstrap":{"node":{"metadata":{"outer":{"name":"Bearer dummy-token"}}}}}]}`
	_, fs, err := redact.ConfigDump([]byte(dump), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if strings.Contains(f.Location, ".name") {
			t.Errorf("location %q shows a nested metadata key", f.Location)
		}
	}
}

func TestErrorTextTooLong(t *testing.T) {
	got := redact.ErrorText(errors.New("token=dummy "+strings.Repeat("x", 64<<10)), "")
	if got != "[error text too long]" {
		t.Errorf("ErrorText = %.60q", got)
	}
}

func TestEmbeddedJSONInMetadataMasked(t *testing.T) {
	dump := `{"configs":[{"bootstrap":{"node":{"metadata":{"outer":{"@type":"type.googleapis.com/google.protobuf.StringValue",` +
		`"value":"{\"name\":\"Bearer dummy-token\"}"}}}}}]}`
	_, fs, err := redact.ConfigDump([]byte(dump), redact.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) == 0 {
		t.Fatal("no finding")
	}
	for _, f := range fs {
		if strings.Contains(f.Location, ".name") {
			t.Errorf("location %q shows a key of the embedded JSON", f.Location)
		}
	}
}

func TestManagedFieldsAnchored(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w", "managedFields": []any{map[string]any{"manager": "m"}},
			"annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"}},
		"spec": map[string]any{"data": map[string]any{"metadata": map[string]any{
			"managedFields": "application-setting",
			"annotations":   map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "app-value"},
		}}},
	}}
	if _, err := redact.Object(u, redact.Options{}); err != nil {
		t.Fatal(err)
	}
	md := u.Object["metadata"].(map[string]any)
	if _, ok := md["managedFields"]; ok {
		t.Error("root managedFields kept")
	}
	if _, ok := md["annotations"].(map[string]any)["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("root last-applied kept")
	}
	inner := u.Object["spec"].(map[string]any)["data"].(map[string]any)["metadata"].(map[string]any)
	if inner["managedFields"] != "application-setting" {
		t.Errorf("spec.data.metadata.managedFields = %v", inner["managedFields"])
	}
	if _, ok := inner["annotations"].(map[string]any)["kubectl.kubernetes.io/last-applied-configuration"]; !ok {
		t.Error("spec.data.metadata last-applied removed")
	}
}
