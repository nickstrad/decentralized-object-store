package merkle

// SetSkipBranch toggles the planted bug that causes Session to ignore child 15
// mismatches and returns a function that restores the previous setting.
func SetSkipBranch(on bool) func() {
	old := skipLastBranch
	skipLastBranch = on
	return func() { skipLastBranch = old }
}
