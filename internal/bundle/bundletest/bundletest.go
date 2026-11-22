// Package bundletest holds test helpers for the support bundle: it extracts
// an archive strictly, scans it for canaries and loads the redaction canary
// corpus. It is imported by tests only.
package bundletest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// CanaryPrefix starts every planted corpus value; AssertNoCanary flags it
// wherever it appears.
const CanaryPrefix = "kgwctl-canary"

// Member is one regular file of an archive.
type Member struct {
	// Name is the path below the archive root.
	Name   string
	Header tar.Header
	Data   []byte
}

// Archive is an extracted bundle.
type Archive struct {
	Root    string
	Members []Member
	// TarSize is the uncompressed size of the tar stream.
	TarSize int64
}

// File returns the data of the member at name below the root.
func (a Archive) File(name string) ([]byte, bool) {
	for _, m := range a.Members {
		if m.Name == name {
			return m.Data, true
		}
	}
	return nil, false
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Extract reads a tar.gz bundle. It fails the test on a duplicate member name
// or a member that is also another member's directory (ignoring case), a
// non-regular entry or a path outside the single root.
func Extract(t *testing.T, r io.Reader) Archive {
	t.Helper()
	a, err := extract(r)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func extract(r io.Reader) (Archive, error) {
	var a Archive
	zr, err := gzip.NewReader(r)
	if err != nil {
		return a, fmt.Errorf("gzip: %w", err)
	}
	cr := &countReader{r: zr}
	tr := tar.NewReader(cr)
	seen, dirs := map[string]bool{}, map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return a, fmt.Errorf("tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return a, fmt.Errorf("member %q: type %q, want regular", h.Name, h.Typeflag)
		}
		clean := path.Clean(h.Name)
		root, rest, ok := strings.Cut(clean, "/")
		if !ok || clean != h.Name || path.IsAbs(h.Name) || root == ".." || rest == "" || strings.HasPrefix(rest, "../") {
			return a, fmt.Errorf("member %q is outside a root", h.Name)
		}
		if a.Root == "" {
			a.Root = root
		} else if root != a.Root {
			return a, fmt.Errorf("member %q is outside root %q", h.Name, a.Root)
		}
		key := strings.ToLower(clean)
		if seen[key] {
			return a, fmt.Errorf("duplicate member %q", h.Name)
		}
		if dirs[key] {
			return a, fmt.Errorf("member %q is also a directory", h.Name)
		}
		for i := range len(key) {
			if key[i] == '/' {
				if seen[key[:i]] {
					return a, fmt.Errorf("member %q is under file %q", h.Name, key[:i])
				}
				dirs[key[:i]] = true
			}
		}
		seen[key] = true
		data, err := io.ReadAll(tr)
		if err != nil {
			return a, fmt.Errorf("member %q: %w", h.Name, err)
		}
		a.Members = append(a.Members, Member{Name: rest, Header: *h, Data: data})
	}
	if _, err := io.Copy(io.Discard, cr); err != nil {
		return a, fmt.Errorf("tar tail: %w", err)
	}
	a.TarSize = cr.n
	return a, nil
}

// AssertNoCanary fails the test for every hit Scan reports.
func AssertNoCanary(t *testing.T, a Archive, canaries []string) {
	t.Helper()
	for _, h := range Scan(a, canaries) {
		t.Error(h)
	}
}

// Scan reports where a canary, or any CanaryPrefix value, appears in the
// root, a member name or member data: raw, in a decoded base64 run, or inside
// a nested gzip member.
func Scan(a Archive, canaries []string) []string {
	needles := append([]string{CanaryPrefix}, canaries...)
	var hits []string
	check := func(where, s string) {
		for _, n := range needles {
			if n != "" && strings.Contains(s, n) {
				hits = append(hits, fmt.Sprintf("%s holds %q", where, n))
			}
		}
	}
	check("root", a.Root)
	for _, m := range a.Members {
		check("member name "+m.Name, m.Name)
		check("member name "+m.Name+" (base64)", DecodeBase64Runs(m.Name))
		scanData(m.Name, m.Data, check, 0)
	}
	return hits
}

func scanData(where string, b []byte, check func(where, s string), depth int) {
	check(where, string(b))
	check(where+" (base64)", DecodeBase64Runs(string(b)))
	if depth < 4 && len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err == nil {
			if inner, err := io.ReadAll(zr); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
				scanData(where+" (gzip)", inner, check, depth+1)
			}
		}
	}
}

// Base64 runs, one regexp per alphabet so a run mixing both still decodes.
var (
	stdRun = regexp.MustCompile(`[A-Za-z0-9+/]{16,}={0,2}`)
	urlRun = regexp.MustCompile(`[A-Za-z0-9_-]{16,}={0,2}`)
)

// DecodeBase64Runs decodes every base64-looking run of s at each of the four
// alignments, in its alphabet, and returns the decoded bytes joined.
func DecodeBase64Runs(s string) string {
	var b strings.Builder
	for _, a := range []struct {
		re  *regexp.Regexp
		enc *base64.Encoding
	}{{stdRun, base64.RawStdEncoding}, {urlRun, base64.RawURLEncoding}} {
		for _, run := range a.re.FindAllString(s, -1) {
			run = strings.TrimRight(run, "=")
			for off := 0; off < 4 && off < len(run); off++ {
				r := run[off:]
				// Whole quanta always decode; the full run also covers a partial tail.
				for _, in := range []string{r[:len(r)-len(r)%4], r} {
					if d, err := a.enc.DecodeString(in); err == nil {
						b.Write(d)
						b.WriteByte('\n')
					}
				}
			}
		}
	}
	return b.String()
}

// Sink methods a corpus case goes through.
const (
	MethodObject     = "Object"
	MethodConfigDump = "ConfigDump"
	MethodJSON       = "JSON"
	MethodPrometheus = "Prometheus"
	MethodText       = "Text"
	MethodError      = "Error"
)

// Expect is a finding a corpus case must report.
type Expect struct{ Rule, Location string }

// Fixture is one case of the canary corpus with the Sink method it feeds.
type Fixture struct {
	Row    int
	Dir    string
	Name   string
	Type   string
	Method string
	Input  []byte
	// ExtraHeaders is the case's redact.Options.ExtraHeaders.
	ExtraHeaders []string
	// Canaries must appear nowhere in the archive.
	Canaries []string
	// FindingsOnly values are map keys: kept in the item, never elsewhere.
	FindingsOnly []string
	// Expect are the findings the case must report.
	Expect []Expect
	// Keep must survive in the written item.
	Keep        []string
	ExpectError string
	ReadError   bool
	Server      string
	// Err is the error for the Error method.
	Err error
}

type fixtureFile struct {
	Row      int       `json:"row"`
	Title    string    `json:"title"`
	Cases    []fixCase `json:"cases"`
	Deferred []struct {
		Part string `json:"part"`
		Task string `json:"task"`
	} `json:"deferred"`
}

type fixCase struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Input   string `json:"input"`
	Options struct {
		ExtraHeaders []string `json:"extraHeaders"`
	} `json:"options"`
	Canaries []struct {
		Value        string `json:"value"`
		Rule         string `json:"rule"`
		Location     string `json:"location"`
		FindingsOnly bool   `json:"findingsOnly"`
	} `json:"canaries"`
	Keep        []string `json:"keep"`
	ExpectError string   `json:"expectError"`
	ReadError   bool     `json:"readError"`
	Server      string   `json:"server"`
	Error       *struct {
		Kind    string `json:"kind"`
		Code    int32  `json:"code"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"error"`
}

var methods = map[string]string{
	"object":       MethodObject,
	"config-dump":  MethodConfigDump,
	"json":         MethodJSON,
	"prometheus":   MethodPrometheus,
	"text":         MethodText,
	"error-text":   MethodError,
	"remote-error": MethodError,
}

// CorpusDir returns the canary corpus directory, internal/redact/testdata/canary.
func CorpusDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate bundletest")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "redact", "testdata", "canary")
}

// Corpus loads every case of the canary corpus, as described in its README.
func Corpus(t *testing.T) []Fixture {
	t.Helper()
	dir := CorpusDir(t)
	dirs, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Fixture
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, d.Name(), "fixture.yaml"))
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		var f fixtureFile
		if err := yaml.UnmarshalStrict(b, &f); err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		for _, c := range f.Cases {
			fx := Fixture{
				Row: f.Row, Dir: d.Name(), Name: c.Name, Type: c.Type, Method: methods[c.Type],
				ExtraHeaders: c.Options.ExtraHeaders, ExpectError: c.ExpectError,
				ReadError: c.ReadError, Server: c.Server, Keep: c.Keep,
			}
			if fx.Method == "" {
				t.Fatalf("%s/%s: unknown type %q", d.Name(), c.Name, c.Type)
			}
			if c.Input != "" {
				if fx.Input, err = os.ReadFile(filepath.Join(dir, d.Name(), c.Input)); err != nil {
					t.Fatal(err)
				}
			}
			for _, cn := range c.Canaries {
				if cn.Rule != "" {
					fx.Expect = append(fx.Expect, Expect{Rule: cn.Rule, Location: cn.Location})
				}
				if cn.FindingsOnly {
					fx.FindingsOnly = append(fx.FindingsOnly, cn.Value)
				} else {
					fx.Canaries = append(fx.Canaries, cn.Value)
				}
			}
			switch c.Type {
			case "error-text":
				fx.Err = errors.New(string(fx.Input))
			case "remote-error":
				fx.Err = remoteErr(t, c)
			}
			out = append(out, fx)
		}
	}
	return out
}

func remoteErr(t *testing.T, c fixCase) error {
	t.Helper()
	e := c.Error
	if e == nil {
		t.Fatalf("%s: remote-error without error", c.Name)
	}
	switch e.Kind {
	case "status":
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: e.Code, Reason: metav1.StatusReason(e.Reason), Message: e.Message,
		}}
	case "log-stream":
		// client-go's Stream returns this shape for a non-2xx response with an unparsed body.
		return fmt.Errorf("%d while accessing %v: %s", e.Code, "https://10.0.0.1:6443/api/v1/namespaces/team-a/pods/gw/log", e.Message)
	}
	t.Fatalf("%s: unknown error kind %q", c.Name, e.Kind)
	return nil
}
