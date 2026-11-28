// Command bundlecheck backs the bundle step of hack/live-check.sh.
//
//	bundlecheck extract <archive> <dir>          extract a bundle; fails on a duplicate or unsafe member
//	bundlecheck scan <archive> <label>=<value>... scan every member name and body for each canary
//	bundlecheck scanfile <file> <label>=<value>... scan one plain file, such as captured stderr
//	bundlecheck prom <file>...                   check each file parses as Prometheus text exposition
//
// scan reports a canary found raw, base64-encoded, inside a decoded base64 run, or inside a gzip
// member or gzip payload. It prints canary labels and member names that carry no canary, never
// canary values; a member whose name carries one is shown by its index.
package main

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
	"strings"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "extract":
		if len(os.Args) != 4 {
			usage()
		}
		err = extract(os.Args[2], os.Args[3])
	case "scan":
		err = scan(os.Stdout, os.Args[2], os.Args[3:])
	case "scanfile":
		err = scanFile(os.Stdout, os.Args[2], os.Args[3:])
	case "prom":
		err = prom(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bundlecheck:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bundlecheck extract <archive> <dir> | scan <archive> <label>=<value>... | scanfile <file> <label>=<value>... | prom <file>...")
	os.Exit(2)
}

type member struct {
	name string
	body []byte
}

// members reads every regular file of a tar.gz; a duplicate or unsafe name is an error.
// Errors name a member by its index, since a name can carry a canary.
func members(archive string) ([]member, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	seen := map[string]bool{}
	var out []member
	for idx := 0; ; idx++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		clean := path.Clean(h.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("unsafe name on member #%d", idx)
		}
		if seen[clean] {
			return nil, fmt.Errorf("member #%d duplicates an earlier member name", idx)
		}
		seen[clean] = true
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("member #%d has type %c, want a regular file", idx, h.Typeflag)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out = append(out, member{clean, b})
	}
}

func extract(archive, dir string) error {
	ms, err := members(archive)
	if err != nil {
		return err
	}
	for _, m := range ms {
		p := filepath.Join(dir, filepath.FromSlash(m.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, m.body, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("extracted %d members\n", len(ms))
	return nil
}

type canary struct{ label, value string }

// scan writes its findings to w.
func scan(w io.Writer, archive string, args []string) error {
	return scanMembers(w, func() ([]member, error) { return members(archive) }, args)
}

// scanFile scans one plain file as a single member named by its base name.
func scanFile(w io.Writer, file string, args []string) error {
	return scanMembers(w, func() ([]member, error) {
		b, err := os.ReadFile(file)
		return []member{{filepath.Base(file), b}}, err
	}, args)
}

// scanMembers parses the canaries, loads the members and writes its findings to w.
func scanMembers(w io.Writer, load func() ([]member, error), args []string) error {
	var cs []canary
	for _, a := range args {
		l, v, ok := strings.Cut(a, "=")
		if !ok || l == "" || len(v) < 8 {
			return fmt.Errorf("canary %q: want <label>=<value of 8 or more bytes>", l)
		}
		cs = append(cs, canary{l, v})
	}
	if len(cs) == 0 {
		return errors.New("no canaries")
	}
	ms, err := load()
	if err != nil {
		return err
	}
	leaks := 0
	for i, m := range ms {
		// A name that carries a canary is shown by its index, never by its text.
		shown := m.name
		for _, c := range cs {
			if how := find([]byte(m.name), c.value, 0); how != "" {
				fmt.Fprintf(w, "LEAK %s in the name of member #%d (%s)\n", c.label, i, how)
				shown = fmt.Sprintf("member #%d", i)
				leaks++
			}
		}
		for _, c := range cs {
			if how := find(m.body, c.value, 0); how != "" {
				fmt.Fprintf(w, "LEAK %s in %s (%s)\n", c.label, shown, how)
				leaks++
			}
		}
	}
	if leaks > 0 {
		return fmt.Errorf("%d canary hits", leaks)
	}
	fmt.Fprintf(w, "scanned %d members for %d canaries (raw, base64, gzip): none found\n", len(ms), len(cs))
	return nil
}

// b64run is a run of base64 (standard or URL alphabet) long enough to hide a canary.
var b64run = regexp.MustCompile(`[A-Za-z0-9+/_-]{12,}={0,2}`)

// find returns how v occurs in b ("" when it does not), looking through gzip and base64 layers.
func find(b []byte, v string, depth int) string {
	if depth > 3 {
		return ""
	}
	if bytes.Contains(b, []byte(v)) {
		return "raw"
	}
	for _, enc := range encodings(v) {
		if bytes.Contains(b, []byte(enc)) {
			return "base64-encoded"
		}
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
			if d, err := io.ReadAll(io.LimitReader(zr, 256<<20)); err == nil {
				if how := find(d, v, depth+1); how != "" {
					return "gzip/" + how
				}
			}
		}
	}
	for _, run := range b64run.FindAll(b, -1) {
		for _, d := range decodings(run) {
			if how := find(d, v, depth+1); how != "" {
				return "base64-decoded/" + how
			}
		}
	}
	return ""
}

// encodings returns the alignment-stable cores of v's base64 forms at the three byte offsets.
func encodings(v string) []string {
	var out []string
	for off := range 3 {
		src := append(bytes.Repeat([]byte{0}, off), v...)
		for _, e := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			s := e.EncodeToString(src)
			// The first chars mix in the padding bytes and the last ones the tail; keep the middle.
			lo, hi := (off*4+2)/3, len(s)-4
			if hi-lo >= 8 {
				out = append(out, s[lo:hi])
			}
		}
	}
	return out
}

// decodings decodes a base64 run with each alphabet, also after dropping up to three leading bytes.
func decodings(run []byte) [][]byte {
	s := strings.TrimRight(string(run), "=")
	var out [][]byte
	for skip := range 4 {
		if len(s)-skip < 12 {
			break
		}
		t := s[skip:]
		t = t[:len(t)-len(t)%4]
		for _, e := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			if d, err := e.DecodeString(t); err == nil {
				out = append(out, d)
			}
		}
	}
	return out
}

// prom parses each file with the strict (legacy name) Prometheus text grammar and wants samples.
func prom(files []string) error {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		p := expfmt.NewTextParser(model.LegacyValidation)
		fams, err := p.TextToMetricFamilies(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		samples := 0
		for _, mf := range fams {
			samples += len(mf.GetMetric())
		}
		if samples == 0 {
			return fmt.Errorf("%s: no samples", f)
		}
		fmt.Printf("%s: %d families, %d samples\n", f, len(fams), samples)
	}
	return nil
}
