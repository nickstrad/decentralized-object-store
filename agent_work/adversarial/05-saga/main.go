package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

type scenarioCheck struct {
	claim, name string
	run         func() string
}

var checks []scenarioCheck

func add(c, n string, f func() string) { checks = append(checks, scenarioCheck{c, n, f}) }
func check(ok bool, f string, a ...any) {
	if !ok {
		panic(fmt.Sprintf(f, a...))
	}
}
func must(err error) {
	if err != nil {
		panic(err.Error())
	}
}
func execute(a scenarioCheck) (broken bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("BROKEN %s %s: %s\n", a.claim, a.name, strings.ReplaceAll(fmt.Sprint(r), "\n", " "))
			broken = true
		}
	}()
	detail := a.run()
	fmt.Printf("HELD %s %s: %s\n", a.claim, a.name, detail)
	return
}
func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "codec":
		codec()
	case "engine":
		engine()
	case "supplement":
		supplement()
	case "migration":
		migration()
	default:
		os.Exit(2)
	}
	bad := false
	for _, a := range checks {
		if execute(a) {
			bad = true
		}
	}
	if bad {
		os.Exit(1)
	}
}

var _ = bytes.Equal
var _ = errors.Is
