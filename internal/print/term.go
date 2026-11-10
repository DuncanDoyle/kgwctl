package print

import (
	"os"

	"golang.org/x/term"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// ColorEnabled reports whether f is a terminal and NO_COLOR is not set (even if empty).
func ColorEnabled(f *os.File) bool {
	return colorEnabled(IsTerminal(f), os.LookupEnv)
}

// colorEnabled is ColorEnabled with the terminal check and environment injected.
func colorEnabled(isTerminal bool, lookup func(string) (string, bool)) bool {
	_, noColor := lookup("NO_COLOR")
	return isTerminal && !noColor
}
