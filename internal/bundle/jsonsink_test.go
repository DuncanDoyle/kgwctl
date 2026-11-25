package bundle

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// publicOnly hides every method outside pb.Sink, as a downstream wrapper would.
type publicOnly struct{ pb.Sink }

// TestWriteJSONBuiltinOnly: a Sink has no public JSON method (D-029). Only a
// built-in Sink and a known artifact write JSON; anything else is a failed item.
func TestWriteJSONBuiltinOnly(t *testing.T) {
	h := newHarness(t, mustLimits(t, 32<<20), redact.Options{}, "")
	var s pb.Sink = h.w.Sink("c")
	if _, ok := s.(interface {
		JSON(context.Context, pb.Path, io.Reader, pb.Source) error
	}); ok {
		t.Error("a Sink still has the public JSON method")
	}
	if err := WriteJSON(bg, s, JSONArtifact(99), pb.Path{"x", "unknown.json"}, strings.NewReader(`{}`), pb.Source{}); !errors.Is(err, errNoJSON) {
		t.Errorf("unknown artifact: err = %v", err)
	}
	if err := WriteJSON(bg, publicOnly{s}, JSONVersion, pb.Path{"x", "wrapped.json"}, strings.NewReader(`{}`), pb.Source{}); !errors.Is(err, errNoJSON) {
		t.Errorf("non-built-in Sink: err = %v", err)
	}
	if err := WriteJSON(bg, s, JSONVersion, pb.Path{"x", "ok.json"}, strings.NewReader(`{"token":"abc"}`), pb.Source{}); err != nil {
		t.Fatal(err)
	}
	h.w.Seal()
	a := h.finish(t, Manifest{}, "c")
	for _, p := range []string{"x/unknown.json", "x/wrapped.json"} {
		if it := itemByPath(h.w.Items(), p); len(it) != 1 || it[0].Status != pb.ItemFailed {
			t.Errorf("%s item %+v, want one failed", p, it)
		}
		if _, ok := a.File(p); ok {
			t.Errorf("archive holds %s", p)
		}
	}
	if b, _ := a.File("x/ok.json"); !strings.Contains(string(b), "[redacted]") || strings.Contains(string(b), "abc") {
		t.Errorf("x/ok.json = %q, want the token redacted", b)
	}
}
