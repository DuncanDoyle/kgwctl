package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCanary = "zq7-canary-value-0001"

// writeArchive writes a tar.gz with one regular file per name and returns its path.
func writeArchive(t *testing.T, names ...string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o600, Size: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("ok")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// forms returns the raw, base64 and gzip renderings of the canary that output must not contain.
func forms(t *testing.T) []string {
	t.Helper()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(testCanary))
	_ = zw.Close()
	return []string{
		testCanary,
		base64.StdEncoding.EncodeToString([]byte(testCanary)),
		base64.URLEncoding.EncodeToString([]byte(testCanary)),
		gz.String(),
	}
}

func assertNoCanary(t *testing.T, out string) {
	t.Helper()
	for _, f := range forms(t) {
		if strings.Contains(out, f) {
			t.Errorf("output contains a canary form (%d bytes): %q", len(f), out)
		}
	}
}

func TestScanHidesCanaryInMemberName(t *testing.T) {
	for name, member := range map[string]string{
		"raw":    "dir/" + testCanary + ".json",
		"base64": "dir/" + base64.StdEncoding.EncodeToString([]byte(testCanary)) + ".json",
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := scan(&out, writeArchive(t, "ok.txt", member), []string{"secret=" + testCanary})
			if err == nil {
				t.Fatal("scan passed; want a canary hit")
			}
			if !strings.Contains(out.String(), "LEAK secret in the name of member #1") {
				t.Errorf("output does not identify the hit: %q", out.String())
			}
			assertNoCanary(t, out.String()+err.Error())
		})
	}
}

func TestMembersErrorsHideCanary(t *testing.T) {
	for name, member := range map[string]string{
		"unsafe":    "../" + testCanary,
		"duplicate": "",
	} {
		t.Run(name, func(t *testing.T) {
			names := []string{member}
			if member == "" {
				names = []string{testCanary, testCanary}
			}
			_, err := members(writeArchive(t, names...))
			if err == nil {
				t.Fatal("members passed; want an error")
			}
			assertNoCanary(t, err.Error())
		})
	}
}

// TestScanFile covers captured command output: a canary in any form fails, clean or empty output passes.
func TestScanFile(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		leak bool
	}{
		"raw":    {"collector logs: failed: token " + testCanary + "\n", true},
		"base64": {"value " + base64.StdEncoding.EncodeToString([]byte(testCanary)) + "\n", true},
		"clean":  {"collector logs: partial\nwrote bundle.tar.gz\n", false},
		"empty":  {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "reader.stderr")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := scanFile(&out, p, []string{"secret=" + testCanary})
			if (err != nil) != tc.leak {
				t.Fatalf("scanFile error %v, want a hit %v", err, tc.leak)
			}
			if tc.leak && !strings.Contains(out.String(), "LEAK secret in reader.stderr") {
				t.Errorf("output does not identify the hit: %q", out.String())
			}
			msg := out.String()
			if err != nil {
				msg += err.Error()
			}
			assertNoCanary(t, msg)
		})
	}
}
