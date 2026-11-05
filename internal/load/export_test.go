package load

import "github.com/DuncanDoyle/kgateway-ui/pkg/cache"

// SetCoreSettledHook sets the core-settled hook and returns a func that clears it.
func SetCoreSettledHook(f func()) func() {
	onCoreSettled = f
	return func() { onCoreSettled = nil }
}

// SetDeadlineHook sets the hook run when the wait sees ctx end and returns a func that clears it.
func SetDeadlineHook(f func(cache.Snapshotter)) func() {
	onDeadline = f
	return func() { onDeadline = nil }
}

// ThrottleDefaults exposes withThrottleDefaults.
var ThrottleDefaults = withThrottleDefaults
