package print_test

import (
	"os"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/print"
)

func TestNotTerminal(t *testing.T) {
	// /dev/null is a character device but not a terminal.
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	reg, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	for _, f := range []*os.File{null, reg} {
		if print.IsTerminal(f) || print.ColorEnabled(f) {
			t.Errorf("%s is not a terminal", f.Name())
		}
	}
}
