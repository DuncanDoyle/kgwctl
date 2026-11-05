package load

// SetCoreSettledHook sets the core-settled hook and returns a func that clears it.
func SetCoreSettledHook(f func()) func() {
	onCoreSettled = f
	return func() { onCoreSettled = nil }
}

// SetDeadlineHook sets the hook run when the wait sees ctx end and returns a func that clears it.
func SetDeadlineHook(f func()) func() {
	onDeadline = f
	return func() { onDeadline = nil }
}
