package print_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/print"
)

// TestSchemaVersion pins the envelope version every kind carries; other tests use the constant.
func TestSchemaVersion(t *testing.T) {
	const want = "kgwctl.kgateway.dev/v1alpha2"
	if print.SchemaVersion != want {
		t.Fatalf("SchemaVersion = %q, want %q", print.SchemaVersion, want)
	}
	var b bytes.Buffer
	if err := print.Envelope(&b, print.FormatYAML, "Version", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "schemaVersion: "+want+"\n") {
		t.Errorf("envelope = %q, want schemaVersion %s", b.String(), want)
	}
}
