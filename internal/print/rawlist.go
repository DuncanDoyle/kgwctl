package print

import (
	"encoding/json"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// RawList writes objs as stored, in a v1 List without managedFields (design §3.1),
// as format "yaml" or "json". It never changes objs.
func RawList(w io.Writer, format string, objs []*unstructured.Unstructured) error {
	items := make([]any, 0, len(objs))
	for _, o := range objs {
		c := o.DeepCopy()
		c.SetManagedFields(nil)
		items = append(items, c.Object)
	}
	l := map[string]any{"apiVersion": "v1", "kind": "List", "items": items, "metadata": map[string]any{"resourceVersion": ""}}
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "    ")
		return enc.Encode(l)
	case FormatYAML:
		b, err := yaml.Marshal(l)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	return fmt.Errorf("unknown list format %q", format)
}
