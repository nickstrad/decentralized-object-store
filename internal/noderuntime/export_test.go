package noderuntime

func SetCanaryUntrackedGoroutine(release chan struct{}) { canaryUntrackedGoroutine = release }
