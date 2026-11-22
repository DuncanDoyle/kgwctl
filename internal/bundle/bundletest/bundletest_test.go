package bundletest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	data string
}

func archive(t *testing.T, es ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.data))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if e.typ == tar.TypeSymlink {
			h.Linkname = "x"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractRejects(t *testing.T) {
	cases := map[string][]entry{
		"duplicate":     {{"r/a.txt", tar.TypeReg, "1"}, {"r/A.TXT", tar.TypeReg, "2"}},
		"symlink":       {{"r/a", tar.TypeSymlink, ""}},
		"directory":     {{"r/d/", tar.TypeDir, ""}},
		"dot-dot":       {{"r/../x.txt", tar.TypeReg, "1"}},
		"absolute":      {{"/r/x.txt", tar.TypeReg, "1"}},
		"no-root":       {{"x.txt", tar.TypeReg, "1"}},
		"second-root":   {{"r/a.txt", tar.TypeReg, "1"}, {"s/b.txt", tar.TypeReg, "1"}},
		"file-then-dir": {{"r/a/b", tar.TypeReg, "1"}, {"r/A/b/c.txt", tar.TypeReg, "2"}},
		"dir-then-file": {{"r/a/b/c.txt", tar.TypeReg, "1"}, {"r/a/B", tar.TypeReg, "2"}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := extract(bytes.NewReader(archive(t, es...))); err == nil {
				t.Error("accepted")
			}
		})
	}
	a, err := extract(bytes.NewReader(archive(t, entry{"r/a/b.txt", tar.TypeReg, "hi"})))
	if err != nil || a.Root != "r" || len(a.Members) != 1 || a.Members[0].Name != "a/b.txt" || string(a.Members[0].Data) != "hi" {
		t.Errorf("valid archive: %+v, %v", a, err)
	}
}

func gz(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestScan(t *testing.T) {
	const c = "kgwctl-canary-scan"
	const plain = "zz-secret-zz" // no prefix, so only the listed canary flags it
	std := base64.StdEncoding.EncodeToString([]byte("??~" + plain + "??~"))
	url := base64.URLEncoding.EncodeToString([]byte(">>?" + plain + ">>?"))
	cases := map[string]Member{
		"name":           {Name: "logs/" + c + ".log"},
		"raw":            {Name: "a.txt", Data: []byte("x " + plain + " y")},
		"std-odd-offset": {Name: "a.txt", Data: []byte("x" + std[:len(std)-1] + "\n")},
		"url-odd-offset": {Name: "a.txt", Data: []byte("abc" + url)},
		"mixed-prefix":   {Name: "a.txt", Data: []byte("secret_a2d3Y3RsLWNhbmFyeS0/P34=")},
		"nested-gzip":    {Name: "a.gz", Data: []byte(gz(t, "inner "+plain))},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Scan(Archive{Root: "r", Members: []Member{m}}, []string{plain}); len(got) == 0 {
				t.Errorf("no hit in %q", m.Data)
			}
		})
	}
	clean := Archive{Root: "r", Members: []Member{{Name: "a.txt", Data: []byte("nothing [redacted] here")}}}
	if got := Scan(clean, []string{plain}); len(got) != 0 {
		t.Errorf("clean archive: %v", got)
	}
	if got := Scan(Archive{Root: "r", Members: []Member{{Name: "a.txt", Data: []byte("ok " + c)}}}, nil); !slices.ContainsFunc(got, func(s string) bool {
		return strings.Contains(s, CanaryPrefix)
	}) {
		t.Errorf("prefix not flagged: %v", got)
	}
}
