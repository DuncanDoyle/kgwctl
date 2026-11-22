package redact

import (
	"bytes"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
)

// forbiddenKinds are never written to a bundle, in any API group.
var forbiddenKinds = map[string]bool{
	"Secret": true, "SecretList": true, "ConfigMap": true, "ConfigMapList": true,
}

// Object redacts a Kubernetes object in place: it drops managedFields and the
// last-applied annotation, then applies the header-value, JWKS, per-kind
// field, key-name and header-pair rules and the pattern pass (design §7).
// Secrets and ConfigMaps, also inside a List, give ErrForbiddenKind and are
// left unchanged.
func Object(u *unstructured.Unstructured, o Options) ([]Finding, error) {
	if err := checkKind(u.Object); err != nil {
		return nil, err
	}
	b, err := json.Marshal(u.Object)
	if err != nil {
		return nil, fmt.Errorf("redact: encode object: %w", err)
	}
	t, err := parseTree(b)
	if err != nil {
		return nil, err
	}
	w := &walker{mode: modeObject, kind: u.GetKind(), opts: o, rec: &recorder{}}
	w.walk(t, "", "", 0)
	var out bytes.Buffer
	t.encode(&out)
	obj := map[string]any{}
	// util/json keeps integers as int64, as unstructured expects.
	if err := utiljson.Unmarshal(out.Bytes(), &obj); err != nil {
		return nil, fmt.Errorf("redact: decode object: %w", err)
	}
	u.Object = obj
	return w.rec.findings(), nil
}

func checkKind(obj map[string]any) error {
	if k, _ := obj["kind"].(string); forbiddenKinds[k] {
		return fmt.Errorf("%w: %s", ErrForbiddenKind, k)
	}
	items, _ := obj["items"].([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if err := checkKind(m); err != nil {
				return err
			}
		}
	}
	return nil
}
