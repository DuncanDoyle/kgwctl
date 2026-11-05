// Package print writes command output: text layouts and the versioned
// JSON/YAML envelope (design §8).
package print

import (
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"
)

// SchemaVersion versions the JSON/YAML envelope; renaming or removing a field bumps it.
const SchemaVersion = "kgwctl.kgateway.dev/v1alpha1"

// Output formats.
const (
	FormatText = "text"
	FormatJSON = "json"
	FormatYAML = "yaml"
)

type envelope struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Item          any    `json:"item"`
}

// Envelope writes item wrapped in the versioned envelope as format, "json" or "yaml".
func Envelope(w io.Writer, format, kind string, item any) error {
	e := envelope{SchemaVersion: SchemaVersion, Kind: kind, Item: item}
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(e)
	case FormatYAML:
		b, err := yaml.Marshal(e)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	return fmt.Errorf("unknown envelope format %q", format)
}
