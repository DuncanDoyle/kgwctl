package print_test

import (
	"os"
	"testing"

	"github.com/DuncanDoyle/kgwctl/internal/print"
)

func TestColorEnabled(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if print.IsTerminal(f) || print.ColorEnabled(f) {
		t.Error("a regular file is not a terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if print.ColorEnabled(os.Stdout) {
		t.Error("NO_COLOR must disable colour")
	}
}
