package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

//go:embed help.txt
var help string

func main() {
	os.Exit(run(os.Args[1:], scenarios(), os.Stdout, os.Stderr))
}

func find(all []sim.Scenario, name string) (sim.Scenario, bool) {
	for _, sc := range all {
		if sc.Name == name {
			return sc, true
		}
	}
	return sim.Scenario{}, false
}

func run(args []string, all []sim.Scenario, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("objsim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	name := fs.String("scenario", "", "")
	seed := fs.Int64("seed", 1, "")
	list := fs.Bool("list", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *list {
		for _, sc := range all {
			fmt.Fprintln(stdout, sc.Name)
		}
		return 0
	}
	sc, ok := find(all, *name)
	if !ok {
		fmt.Fprintf(stderr, "objsim: unknown scenario %q\n", *name)
		return 2
	}
	s := sc.Run(*seed)
	d := s.Digests()
	fmt.Fprintf(stdout, "scenario=%s seed=%d steps=%d time=%d\nevents=%x\nstate=%x\n", sc.Name, *seed, s.Steps(), s.Now(), d.Events, d.State)
	if err := s.Verify(); err != nil {
		fmt.Fprintf(stderr, "objsim: %v\n", err)
		return 1
	}
	return 0
}
