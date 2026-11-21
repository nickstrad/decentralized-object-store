package a

func Local() {
	panic := func(string) {}
	panic("not the builtin")
}
