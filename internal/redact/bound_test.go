package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestIndentedSize(t *testing.T) {
	for _, in := range []string{
		`{}`, `[]`, `{"a":[]}`, `{"a":{},"b":[1,2,{"c":"x\"]["}]}`, `[[[[]]]]`, `"s"`, `1`,
		`{"k":"v\\","l":[{"m":null},true]}`,
	} {
		var out bytes.Buffer
		if err := json.Indent(&out, []byte(in), "", "  "); err != nil {
			t.Fatal(err)
		}
		if got, want := indentedSize([]byte(in)), int64(out.Len()+1); got != want {
			t.Errorf("%s: size %d, want %d", in, got, want)
		}
	}
}

func TestIndentedCompactPastMax(t *testing.T) {
	deep := strings.Repeat("[", 500) + strings.Repeat("]", 500)
	out, _, err := ConfigDump([]byte(deep), Options{MaxSize: 4096})
	if err != nil || string(out) != deep+"\n" {
		t.Errorf("err %v, %d bytes", err, len(out))
	}
	out, _, _ = JSON([]byte(`{"a":[1]}`), Options{MaxSize: 4096})
	if string(out) != "{\n  \"a\": [\n    1\n  ]\n}\n" {
		t.Errorf("small document not indented: %q", out)
	}
}
