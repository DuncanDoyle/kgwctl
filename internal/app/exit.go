package app

import "fmt"

// ExitError ends a command with exit code Code. Execute prints "Error: <Err>"
// only when Err is set, so a command that already reported can exit quietly.
type ExitError struct {
	Code int
	Err  error
}

// Error returns Err's text, or the exit code when Err is nil.
func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

// Unwrap returns Err.
func (e *ExitError) Unwrap() error { return e.Err }
