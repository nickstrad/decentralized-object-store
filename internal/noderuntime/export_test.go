package noderuntime

// SetCanaryUntrackedGoroutine injects a leaked goroutine into Runtime.Start
// for the shutdown leak-check canary test.
func SetCanaryUntrackedGoroutine(release chan struct{}) { canaryUntrackedGoroutine = release }
