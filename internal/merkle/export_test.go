package merkle

func SetSkipBranch(on bool) func() {
	old := skipLastBranch
	skipLastBranch = on
	return func() { skipLastBranch = old }
}
