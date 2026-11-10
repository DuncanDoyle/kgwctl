package print

import "os"

// IsTerminal reports whether f is a character device, such as a terminal.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ColorEnabled reports whether f is a terminal and NO_COLOR is unset or empty.
func ColorEnabled(f *os.File) bool {
	return os.Getenv("NO_COLOR") == "" && IsTerminal(f)
}
